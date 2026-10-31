package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aaliyan1230/go-rig/internal/app"
	"github.com/chzyer/readline"
)

type lineInput interface {
	Readline() (string, error)
	Close() error
}

func newReadlineInput(stdin *os.File, stdout, stderr io.Writer) (lineInput, error) {
	cancelable := readline.NewCancelableStdin(stdin)
	instance, err := readline.NewEx(&readline.Config{
		Prompt: "you> ", Stdin: cancelable, Stdout: stdout, Stderr: stderr,
		HistoryLimit: 500, HistoryFile: "", InterruptPrompt: "^C", EOFPrompt: "\n",
		// Completion is deliberately disabled; only editing and recall are used.
		AutoComplete: noCompletion{},
	})
	if err != nil {
		cancelable.Close()
		return nil, err
	}
	return &readlineInput{Instance: instance, stdin: cancelable}, nil
}

type readlineInput struct {
	*readline.Instance
	stdin *readline.CancelableStdin
}

func (r *readlineInput) Close() error {
	// Readline wraps stdin internally; explicitly unblock our reader first.
	r.stdin.Close()
	return r.Instance.Close()
}

type noCompletion struct{}

func (noCompletion) Do([]rune, int) ([][]rune, int) { return nil, 0 }

func interactiveLoop(conversation *app.Conversation, input lineInput, stdout, stderr io.Writer, newContext func() (context.Context, context.CancelFunc)) int {
	printSession(conversation, stdout)
	fmt.Fprintln(stdout, "Type /help for commands.")
	for {
		line, err := input.Readline()
		if errors.Is(err, readline.ErrInterrupt) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return 0
		}
		if err != nil {
			fmt.Fprintf(stderr, "error: terminal input: %v\n", err)
			return 1
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "/") {
			if handleCommand(conversation, trimmed, stdout, stderr) {
				return 0
			}
			continue
		}
		ctx, stop := newContext()
		renderer := eventRenderer{stdout: stdout, stderr: stderr, interactive: true}
		err = conversation.Prompt(ctx, line, renderer.Render)
		stop()
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "Canceled. Changes already made are preserved.")
		} else if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
	}
}

func handleCommand(conversation *app.Conversation, line string, stdout, stderr io.Writer) bool {
	fields := strings.Fields(line)
	command := fields[0]
	if command != "/model" && len(fields) != 1 {
		fmt.Fprintf(stderr, "usage: %s\n", command)
		return false
	}
	switch command {
	case "/quit":
		return true
	case "/help":
		fmt.Fprintln(stdout, "/help  /quit  /session  /new  /model [name]\nArrow keys: edit/recall input. Ctrl+C: clear input or cancel work. Ctrl+D: quit on empty input.")
	case "/session":
		printSession(conversation, stdout)
	case "/model":
		if len(fields) == 1 {
			fmt.Fprintf(stdout, "Model: %s\n", conversation.Info().Model)
			break
		}
		if len(fields) != 2 {
			fmt.Fprintln(stderr, "usage: /model <name>")
			break
		}
		if err := conversation.SetModel(fields[1]); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
		} else {
			fmt.Fprintf(stdout, "Model: %s (used for subsequent requests)\n", conversation.Info().Model)
		}
	case "/new":
		if err := conversation.NewSession(context.Background()); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
		} else {
			printSession(conversation, stdout)
		}
	default:
		fmt.Fprintf(stderr, "unknown command: %s; type /help\n", command)
	}
	return false
}

func printSession(conversation *app.Conversation, stdout io.Writer) {
	info := conversation.Info()
	persistence := "disabled (memory only)"
	if info.PersistenceEnabled {
		persistence = "enabled"
	}
	fmt.Fprintf(stdout, "Session: %s\nFile: %s\nDirectory: %s\nModel: %s\nMessages: %d\nPersistence: %s\n", info.Metadata.ID, info.Metadata.Path, info.Cwd, info.Model, info.MessageCount, persistence)
}
