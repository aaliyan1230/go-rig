package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/config"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/openai"
	"github.com/aaliyan1230/go-rig/internal/provider"
	"github.com/aaliyan1230/go-rig/internal/session"
	builtintools "github.com/aaliyan1230/go-rig/internal/tools"
)

func main() {
	prompt := flag.String("p", "", "prompt to send (print mode)")
	model := flag.String("model", "", "model name")
	apiKey := flag.String("api-key", "", "API key")
	baseURL := flag.String("base-url", "", "API base URL")
	continueSession := flag.Bool("continue", false, "continue the newest session for the working directory")
	sessionDir := flag.String("session-dir", "", "base directory for sessions (default: user config directory)")
	flag.Parse()

	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: harness -p \"your prompt\"")
		os.Exit(1)
	}

	cfg := config.DefaultConfig()
	if *model != "" {
		cfg.Model.Name = *model
	}

	exitCode := runWithOptions(cfg, *prompt, *apiKey, *baseURL, *continueSession, *sessionDir, os.Stdout, os.Stderr)
	os.Exit(exitCode)
}

// run preserves the original testable one-shot entry point, using a fresh
// default session. The flag-aware CLI calls runWithOptions below.
func run(cfg config.Config, prompt, apiKeyFlag, baseURL string, stdout, stderr io.Writer) int {
	return runWithOptions(cfg, prompt, apiKeyFlag, baseURL, false, "", stdout, stderr)
}

// runWithOptions opens the appropriate transcript, executes one prompt, and
// prints the streaming response. Session startup failures are fatal; append
// failures are handled later by streamPromptWithSession so a model run can
// continue. Extracted from main() for testability.
func runWithOptions(cfg config.Config, prompt, apiKeyFlag, baseURL string, continueSession bool, sessionDir string, stdout, stderr io.Writer) int {
	key, err := openai.ResolveAPIKey(apiKeyFlag)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	client, err := openai.NewClient(key, baseURL)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	workingDir := cfg.Env.WorkingDir
	if workingDir == "" {
		workingDir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "error: get working directory: %v\n", err)
			return 1
		}
	}
	workingDir, err = filepath.Abs(workingDir)
	if err != nil {
		fmt.Fprintf(stderr, "error: resolve working directory: %v\n", err)
		return 1
	}
	workingDir = filepath.Clean(workingDir)
	localEnv := env.NewLocalEnvWithShell(workingDir, cfg.Env.ShellPath)
	tools := configuredTools(cfg.Tools, localEnv)

	if sessionDir == "" {
		sessionDir, err = os.UserConfigDir()
		if err != nil {
			fmt.Fprintf(stderr, "error: resolve user config directory: %v\n", err)
			return 1
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
		fmt.Fprintf(stderr, "error: session startup: %v\n", err)
		return 1
	}

	return streamPromptWithSession(client, cfg, prompt, tools, activeSession, stdout, stderr)
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
type sessionSink interface {
	Messages() []message.AgentMessage
	Append(context.Context, message.AgentMessage) error
	Close() error
}

func streamPrompt(client provider.ModelClient, cfg config.Config, prompt string, tools []agent.Tool, stdout, stderr io.Writer) int {
	return streamPromptWithSession(client, cfg, prompt, tools, nil, stdout, stderr)
}

func streamPromptWithSession(client provider.ModelClient, cfg config.Config, prompt string, tools []agent.Tool, activeSession sessionSink, stdout, stderr io.Writer) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if activeSession != nil {
		defer func() {
			if err := activeSession.Close(); err != nil {
				fmt.Fprintf(stderr, "warning: close session: %v\n", err)
			}
		}()
	}

	promptMessage := message.UserMessage{
		Content:   []message.ContentBlock{message.TextContent{Text: prompt}},
		Timestamp: time.Now().UnixMilli(),
	}
	events := agent.Run(
		ctx,
		client,
		[]message.AgentMessage{promptMessage},
		agent.AgentContext{SystemPrompt: cfg.Agent.SystemPrompt, Messages: sessionMessages(activeSession), Tools: tools},
		agent.RunConfig{Model: cfg.Model.Name, MaxTokens: cfg.Model.MaxTokens, MaxTurns: cfg.Agent.MaxTurns},
	)

	persistenceEnabled := activeSession != nil
	printedAssistantText := false
	exitCode := 0
	for event := range events {
		switch e := event.(type) {
		case agent.MessageStart:
			if _, ok := e.Message.(message.AssistantMessage); ok {
				printedAssistantText = false
			}
		case agent.MessageUpdate:
			if delta, ok := e.Event.(provider.TextDelta); ok {
				fmt.Fprint(stdout, delta.Delta)
				printedAssistantText = true
			}
		case agent.MessageEnd:
			if persistenceEnabled {
				if err := activeSession.Append(ctx, e.Message); err != nil {
					fmt.Fprintf(stderr, "warning: session persistence disabled: %v\n", err)
					persistenceEnabled = false
				}
			}
			if assistant, ok := e.Message.(message.AssistantMessage); ok {
				if !printedAssistantText {
					fmt.Fprint(stdout, assistantText(assistant))
					printedAssistantText = assistantText(assistant) != ""
				}
				if printedAssistantText {
					fmt.Fprintln(stdout)
				}
			}
		case agent.ToolExecStart:
			fmt.Fprintf(stderr, "tool: %s %s\n", e.ToolName, string(e.Args))
		case agent.ToolExecEnd:
			if e.IsError {
				fmt.Fprintf(stderr, "tool: %s failed\n", e.ToolName)
			} else {
				fmt.Fprintf(stderr, "tool: %s complete\n", e.ToolName)
			}
		case agent.AgentEnd:
			if e.Err != nil {
				fmt.Fprintf(stderr, "error: %v\n", e.Err)
				exitCode = 1
			}
		}
	}

	return exitCode
}

func sessionMessages(activeSession sessionSink) []message.AgentMessage {
	if activeSession == nil {
		return nil
	}
	return activeSession.Messages()
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
