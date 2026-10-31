package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/app"
	"github.com/aaliyan1230/go-rig/internal/config"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/openai"
	sysprompt "github.com/aaliyan1230/go-rig/internal/prompt"
	"github.com/aaliyan1230/go-rig/internal/provider"
	"github.com/aaliyan1230/go-rig/internal/session"
	builtintools "github.com/aaliyan1230/go-rig/internal/tools"
	"github.com/chzyer/readline"
)

func main() {
	os.Exit(cli(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func cli(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("harness", flag.ContinueOnError)
	flags.SetOutput(stderr)
	prompt := flags.String("p", "", "prompt to send (print mode; omit for interactive mode)")
	model := flags.String("model", "", "model name")
	apiKey := flags.String("api-key", "", "API key")
	baseURL := flags.String("base-url", "", "API base URL")
	continueSession := flags.Bool("continue", false, "continue the newest session for the working directory")
	sessionDir := flags.String("session-dir", "", "base directory for sessions (default: user config directory)")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}
	printMode := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "p" {
			printMode = true
		}
	})
	if flags.NArg() != 0 || (printMode && *prompt == "") {
		fmt.Fprintln(stderr, "usage: harness [-p \"your prompt\"] [flags]")
		return 1
	}
	if !printMode {
		out, ok := stdout.(*os.File)
		if !ok || !readline.IsTerminal(int(stdin.Fd())) || !readline.IsTerminal(int(out.Fd())) {
			fmt.Fprintln(stderr, "error: interactive mode requires terminal stdin and stdout; use -p \"your prompt\" for print mode")
			return 1
		}
	}
	cfg := config.DefaultConfig()
	if *model != "" {
		cfg.Model.Name = *model
	}

	if printMode {
		return runWithOptions(cfg, *prompt, *apiKey, *baseURL, *continueSession, *sessionDir, stdout, stderr)
	}
	conversation, err := openConversation(cfg, *apiKey, *baseURL, *continueSession, *sessionDir, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer closeConversation(conversation, stderr)
	input, err := newReadlineInput(stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: terminal input: %v\n", err)
		return 1
	}
	defer input.Close()
	return interactiveLoop(conversation, input, stdout, stderr, interruptContext)
}

// run preserves the original testable one-shot entry point, using a fresh
// default session. The flag-aware CLI calls runWithOptions below.
func run(cfg config.Config, prompt, apiKeyFlag, baseURL string, stdout, stderr io.Writer) int {
	return runWithOptions(cfg, prompt, apiKeyFlag, baseURL, false, "", stdout, stderr)
}

// runWithOptions opens the appropriate transcript, executes one prompt, and
// prints the streaming response. Session startup failures are fatal; append
// failures are handled by the shared conversation controller.
func runWithOptions(cfg config.Config, prompt, apiKeyFlag, baseURL string, continueSession bool, sessionDir string, stdout, stderr io.Writer) int {
	conversation, err := openConversation(cfg, apiKeyFlag, baseURL, continueSession, sessionDir, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer closeConversation(conversation, stderr)
	return printPrompt(conversation, prompt, stdout, stderr)
}

func openConversation(cfg config.Config, apiKeyFlag, baseURL string, continueSession bool, sessionDir string, stderr io.Writer) (*app.Conversation, error) {
	key, err := openai.ResolveAPIKey(apiKeyFlag)
	if err != nil {
		return nil, err
	}

	client, err := openai.NewClient(key, baseURL)
	if err != nil {
		return nil, err
	}

	workingDir := cfg.Env.WorkingDir
	if workingDir == "" {
		workingDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
	}
	workingDir, err = filepath.Abs(workingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	workingDir = filepath.Clean(workingDir)
	localEnv := env.NewLocalEnvWithShell(workingDir, cfg.Env.ShellPath)
	tools := configuredTools(cfg.Tools, localEnv)

	if sessionDir == "" {
		sessionDir, err = os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user config directory: %w", err)
		}
	} else if !filepath.IsAbs(sessionDir) {
		sessionDir = filepath.Join(workingDir, sessionDir)
	}

	store := session.NewStore(sessionDir)
	var activeSession *session.Session
	if continueSession {
		activeSession, err = store.OpenLatest(context.Background(), workingDir)
	} else {
		activeSession, err = store.Create(context.Background(), workingDir)
	}
	if err != nil {
		return nil, fmt.Errorf("session startup: %w", err)
	}

	build := func(ctx context.Context) string {
		return systemPrompt(ctx, cfg.Agent, localEnv, tools, workingDir, stderr)
	}
	return app.New(app.Options{
		Client: client, Config: agent.RunConfig{Model: cfg.Model.Name, MaxTokens: cfg.Model.MaxTokens, MaxTurns: cfg.Agent.MaxTurns},
		Tools: tools, SystemPrompt: build(context.Background()), Cwd: workingDir, Session: activeSession,
		CreateSession:     func(ctx context.Context) (app.Session, error) { return store.Create(ctx, workingDir) },
		BuildSystemPrompt: build,
		Warn:              func(err error) { fmt.Fprintf(stderr, "warning: %v\n", err) },
	}), nil
}

// systemPrompt loads project context files and renders the system prompt.
// Unreadable context files are reported as warnings; they never stop a run.
func systemPrompt(ctx context.Context, cfg config.AgentConfig, environment env.Env, tools []agent.Tool, workingDir string, stderr io.Writer) string {
	contextFiles, warnings := sysprompt.LoadContextFiles(ctx, environment, workingDir)
	for _, w := range warnings {
		fmt.Fprintf(stderr, "warning: context file: %v\n", w)
	}
	return sysprompt.Build(sysprompt.Options{
		CustomPrompt: cfg.SystemPrompt,
		Tools:        tools,
		ContextFiles: contextFiles,
		Cwd:          workingDir,
	})
}

func configuredTools(cfg config.ToolConfig, environment env.Env) []agent.Tool {
	tools := make([]agent.Tool, 0, 4)
	if cfg.Read {
		tools = append(tools, builtintools.NewReadTool(environment))
	}
	if cfg.Write {
		tools = append(tools, builtintools.NewWriteTool(environment))
	}
	if cfg.Edit {
		tools = append(tools, builtintools.NewEditTool(environment))
	}
	if cfg.Bash {
		tools = append(tools, builtintools.NewBashTool(environment))
	}
	return tools
}

// sessionSink is the small persistence surface consumed by the CLI. Keeping
// it local makes append-failure behavior directly testable without changing
// the agent loop or the on-disk session implementation.
type sessionSink = app.Session

func streamPrompt(client provider.ModelClient, cfg config.Config, prompt string, tools []agent.Tool, stdout, stderr io.Writer) int {
	return streamPromptWithSession(client, cfg, cfg.Agent.SystemPrompt, prompt, tools, nil, stdout, stderr)
}

func streamPromptWithSession(client provider.ModelClient, cfg config.Config, systemPrompt, prompt string, tools []agent.Tool, activeSession sessionSink, stdout, stderr io.Writer) int {
	conversation := app.New(app.Options{
		Client: client, Config: agent.RunConfig{Model: cfg.Model.Name, MaxTokens: cfg.Model.MaxTokens, MaxTurns: cfg.Agent.MaxTurns},
		SystemPrompt: systemPrompt, Tools: tools, Session: activeSession,
		Warn: func(err error) { fmt.Fprintf(stderr, "warning: %v\n", err) },
	})
	defer closeConversation(conversation, stderr)
	return printPrompt(conversation, prompt, stdout, stderr)
}

func printPrompt(conversation *app.Conversation, prompt string, stdout, stderr io.Writer) int {
	ctx, stop := interruptContext()
	defer stop()
	renderer := eventRenderer{stdout: stdout, stderr: stderr}
	if err := conversation.Prompt(ctx, prompt, renderer.Render); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func closeConversation(conversation *app.Conversation, stderr io.Writer) {
	if err := conversation.Close(); err != nil {
		fmt.Fprintf(stderr, "warning: close session: %v\n", err)
	}
}

func assistantText(assistant message.AssistantMessage) string {
	var text string
	for _, block := range assistant.Content {
		if content, ok := block.(message.TextContent); ok {
			text += content.Text
		}
	}
	return text
}
