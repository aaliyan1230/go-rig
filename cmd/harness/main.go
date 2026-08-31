package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/config"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/openai"
	"github.com/aaliyan1230/go-rig/internal/provider"
	builtintools "github.com/aaliyan1230/go-rig/internal/tools"
)

func main() {
	prompt := flag.String("p", "", "prompt to send (print mode)")
	model := flag.String("model", "", "model name")
	apiKey := flag.String("api-key", "", "API key")
	baseURL := flag.String("base-url", "", "API base URL")
	flag.Parse()

	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: harness -p \"your prompt\"")
		os.Exit(1)
	}

	cfg := config.DefaultConfig()
	if *model != "" {
		cfg.Model.Name = *model
	}

	exitCode := run(cfg, *prompt, *apiKey, *baseURL, os.Stdout, os.Stderr)
	os.Exit(exitCode)
}

// run executes a single-shot prompt and prints the streaming response.
// Extracted from main() for testability.
func run(cfg config.Config, prompt, apiKeyFlag, baseURL string, stdout, stderr io.Writer) int {
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
	localEnv := env.NewLocalEnv(workingDir)
	tools := make([]agent.Tool, 0, 1)
	if cfg.Tools.Read {
		tools = append(tools, builtintools.NewReadTool(localEnv))
	}

	return streamPrompt(client, cfg, prompt, tools, stdout, stderr)
}

func streamPrompt(client provider.ModelClient, cfg config.Config, prompt string, tools []agent.Tool, stdout, stderr io.Writer) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	promptMessage := message.UserMessage{
		Content:   []message.ContentBlock{message.TextContent{Text: prompt}},
		Timestamp: time.Now().UnixMilli(),
	}
	events := agent.Run(
		ctx,
		client,
		[]message.AgentMessage{promptMessage},
		agent.AgentContext{SystemPrompt: cfg.Agent.SystemPrompt, Tools: tools},
		agent.RunConfig{Model: cfg.Model.Name, MaxTokens: cfg.Model.MaxTokens, MaxTurns: cfg.Agent.MaxTurns},
	)

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

func assistantText(assistant message.AssistantMessage) string {
	var text string
	for _, block := range assistant.Content {
		if content, ok := block.(message.TextContent); ok {
			text += content.Text
		}
	}
	return text
}
