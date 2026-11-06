package main

import (
	"context"
	"errors"
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
	"github.com/aaliyan1230/go-rig/internal/policy"
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
	cfg := config.DefaultConfig()
	flags := flag.NewFlagSet("harness", flag.ContinueOnError)
	flags.SetOutput(stderr)
	prompt := flags.String("p", "", "prompt to send (print mode; omit for interactive mode)")
	model := flags.String("model", "", "model name")
	apiKey := flags.String("api-key", "", "API key")
	baseURL := flags.String("base-url", "", "API base URL")
	continueSession := flags.Bool("continue", false, "continue the newest session for the working directory")
	sessionDir := flags.String("session-dir", "", "base directory for sessions (default: user config directory)")
	controls := policy.Defaults()
	toolList := flags.String("tools", "", "comma-separated exact tool allowlist")
	flags.StringVar(&controls.ExcludeTools, "exclude-tools", "", "comma-separated tool denylist")
	flags.BoolVar(&controls.NoTools, "no-tools", false, "disable all tools")
	flags.StringVar(&controls.Approval, "approval", "none", "tool approval: none or ask")
	flags.StringVar(&controls.PathScope, "path-scope", "all", "filesystem scope: all or workspace (does not constrain bash)")
	flags.StringVar(&controls.ProjectTrust, "project-trust", "auto", "project instructions: auto, ask, yes, or no")
	flags.BoolVar(&controls.NoContextFiles, "no-context-files", false, "skip project instruction discovery")
	maxTurns := flags.Int("max-turns", cfg.Agent.MaxTurns, "model requests per renewable batch")
	bashTimeout := flags.Int("bash-timeout", cfg.Env.BashTimeout, "default bash timeout in seconds; a tool call may override it")
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
		if f.Name == "tools" {
			controls.Tools = toolList
		}
	})
	if err := controls.Validate(); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if *maxTurns <= 0 || *bashTimeout <= 0 {
		fmt.Fprintln(stderr, "error: --max-turns and --bash-timeout must be positive")
		return 1
	}
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
	cfg.Agent.MaxTurns = *maxTurns
	cfg.Env.BashTimeout = *bashTimeout
	if *model != "" {
		cfg.Model.Name = *model
	}

	var input *readlineInput
	var confirm policy.Confirm
	editorOutput := stdout
	if printMode {
		editorOutput = stderr
	}
	if errFile, ok := stderr.(*os.File); ok && readline.IsTerminal(int(stdin.Fd())) && readline.IsTerminal(int(errFile.Fd())) {
		var err error
		input, err = newReadlineInput(stdin, editorOutput, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "error: terminal input: %v\n", err)
			return 1
		}
		defer input.Close()
		confirm = input.Confirm
	}
	// Interactive stdout is already checked above; stderr may be redirected.
	if !printMode && input == nil {
		var err error
		input, err = newReadlineInput(stdin, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "error: terminal input: %v\n", err)
			return 1
		}
		defer input.Close()
		confirm = input.Confirm
	}
	conversation, err := openConversationControlled(cfg, *apiKey, *baseURL, *continueSession, *sessionDir, controls, confirm, stderr)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0
		}
		if errors.Is(err, context.Canceled) {
			return 1
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer closeConversation(conversation, stderr)
	if printMode {
		fmt.Fprintln(stderr, conversation.Info().Controls)
		return printPrompt(conversation, *prompt, stdout, stderr)
	}
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
	return openConversationControlled(cfg, apiKeyFlag, baseURL, continueSession, sessionDir, policy.Defaults(), nil, stderr)
}

func openConversationControlled(cfg config.Config, apiKeyFlag, baseURL string, continueSession bool, sessionDir string, controls policy.Controls, confirm policy.Confirm, stderr io.Writer) (*app.Conversation, error) {
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

	if sessionDir == "" {
		sessionDir, err = os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user config directory: %w", err)
		}
	} else if !filepath.IsAbs(sessionDir) {
		sessionDir = filepath.Join(workingDir, sessionDir)
	}
	var environment env.Env = localEnv
	var cleanup func() error
	if controls.PathScope == "workspace" {
		workspace, err := env.NewWorkspaceEnv(workingDir, cfg.Env.ShellPath)
		if err != nil {
			return nil, err
		}
		environment = workspace
		cleanup = workspace.Close
	}
	succeeded := false
	defer func() {
		if !succeeded && cleanup != nil {
			cleanup()
		}
	}()
	all := configuredTools(config.ToolConfig{Read: true, Write: true, Edit: true, Bash: true}, environment)
	defaults := configuredTools(cfg.Tools, environment)
	for i, t := range all {
		if t.ToolName() == "bash" {
			all[i] = builtintools.NewBashToolWithTimeout(environment, cfg.Env.BashTimeout)
		}
	}
	tools, err := policy.Select(all, defaults, controls)
	if err != nil {
		return nil, err
	}
	summary := controls.Summary(tools)
	warn := func(err error) { fmt.Fprintf(stderr, "warning: %v\n", err) }
	trust := &policy.TrustStore{Base: sessionDir}
	build := func(ctx context.Context) (string, error) {
		var files []sysprompt.ContextFile
		if !controls.NoContextFiles {
			paths, warnings := sysprompt.DiscoverContextFiles(ctx, localEnv, workingDir)
			for _, w := range warnings {
				warn(w)
			}
			allow, err := trust.Resolve(ctx, controls.ProjectTrust, workingDir, paths, confirm, warn)
			if err != nil {
				return "", err
			}
			if allow {
				files, warnings = sysprompt.ReadContextFiles(ctx, localEnv, paths)
				for _, w := range warnings {
					warn(w)
				}
			}
		}
		return sysprompt.Build(sysprompt.Options{CustomPrompt: cfg.Agent.SystemPrompt, Tools: tools, ContextFiles: files, Cwd: workingDir}) + "\n\n<execution_controls>\n" + summary + fmt.Sprintf("\nModel requests per renewable batch: %d. Bash default timeout: %d seconds.\n</execution_controls>", cfg.Agent.MaxTurns, effectiveBashTimeout(cfg.Env.BashTimeout)), nil
	}
	system, err := build(context.Background())
	if err != nil {
		return nil, err
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

	broker := policy.NewBroker()
	var ask policy.Confirm
	if confirm != nil {
		ask = broker.Ask
	}
	succeeded = true
	return app.New(app.Options{
		Client: client, Config: agent.RunConfig{Model: cfg.Model.Name, MaxTokens: cfg.Model.MaxTokens, MaxTurns: cfg.Agent.MaxTurns, BeforeToolCall: policy.ApprovalGate(controls.Approval, ask, tools...)},
		Tools: tools, SystemPrompt: system, Cwd: workingDir, Session: activeSession,
		CreateSession:     func(ctx context.Context) (app.Session, error) { return store.Create(ctx, workingDir) },
		BuildSystemPrompt: build,
		Warn:              warn, Broker: broker, Confirm: confirm, Controls: summary, Cleanup: cleanup,
	}), nil
}

func effectiveBashTimeout(seconds int) int {
	if seconds <= 0 {
		return builtintools.DefaultBashTimeoutSeconds
	}
	return seconds
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
	result, err := conversation.Prompt(ctx, prompt, renderer.Render)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if result.Status == agent.EndTurnLimit {
		printPaused(conversation, stderr)
		return 2
	}
	return 0
}

func printPaused(conversation *app.Conversation, out io.Writer) {
	info := conversation.Info()
	fmt.Fprintln(out, "Paused at the turn checkpoint. Use /continue to grant another batch.")
	if info.PersistenceEnabled {
		fmt.Fprintf(out, "Progress saved to %s\nRestart from %s with --continue and the same --session-dir/control flags, then enter /continue.\n", info.Metadata.Path, info.Cwd)
	} else {
		fmt.Fprintln(out, "Persistence is disabled: complete progress is in memory only and may be lost on restart.")
	}
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
