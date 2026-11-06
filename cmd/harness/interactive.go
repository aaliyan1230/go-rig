package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/app"
	"github.com/aaliyan1230/go-rig/internal/policy"
	"github.com/chzyer/readline"
)

type lineInput interface {
	Readline() (string, error)
	Close() error
}

func newReadlineInput(stdin *os.File, stdout, stderr io.Writer) (*readlineInput, error) {
	r := &readlineInput{file: stdin, stdout: stdout, stderr: stderr}
	if err := r.init(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *readlineInput) init() error {
	reader, err := newTerminalReader(r.file)
	if err != nil {
		return err
	}
	cancelable := readline.NewCancelableStdin(reader)
	r.output = &terminalOutput{target: r.stdout}
	instance, err := readline.NewEx(&readline.Config{
		Prompt: "you> ", Stdin: cancelable, Stdout: r.output, Stderr: r.stderr,
		HistoryLimit: 500, HistoryFile: "", InterruptPrompt: "^C", EOFPrompt: "\n",
		DisableAutoSaveHistory: true,
		// Completion is deliberately disabled; only editing and recall are used.
		AutoComplete: noCompletion{},
	})
	if err != nil {
		cancelable.Close()
		reader.Close()
		return err
	}
	r.instance = instance
	r.stdin = cancelable
	r.reader = reader
	for _, line := range r.history {
		instance.SaveHistory(line)
	}
	return nil
}

type readlineInput struct {
	instance       *readline.Instance
	stdin          *readline.CancelableStdin
	reader         io.ReadCloser
	file           *os.File
	stdout, stderr io.Writer
	history        []string
	eof            bool
	inputErr       error
	output         *terminalOutput
}

// Keep readline's configuration immutable while its I/O goroutines run.
// Only the synchronized output destination and its mutex-protected prompt vary.
type terminalOutput struct {
	mu     sync.Mutex
	target io.Writer
}

func (w *terminalOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.target.Write(p)
}
func (w *terminalOutput) set(out io.Writer) { w.mu.Lock(); defer w.mu.Unlock(); w.target = out }

func (r *readlineInput) Close() error {
	if r.instance == nil {
		return nil
	}
	r.reader.Close()
	r.stdin.Close()
	err := r.instance.Close()
	r.instance = nil
	return err
}

func (r *readlineInput) read(ctx context.Context) (string, error) {
	if r.instance == nil {
		if err := r.init(); err != nil {
			return "", err
		}
	}
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	instance := r.instance
	go func() { line, err := instance.Readline(); done <- result{line, err} }()
	select {
	case answer := <-done:
		return answer.line, answer.err
	case <-ctx.Done():
		r.Close()
		<-done
		return "", ctx.Err()
	}
}

func (r *readlineInput) Readline() (string, error) {
	line, err := r.read(context.Background())
	if err == nil && strings.TrimSpace(line) != "" {
		r.history = append(r.history, line)
		if len(r.history) > 500 {
			r.history = r.history[len(r.history)-500:]
		}
		r.instance.SaveHistory(line)
	}
	return line, err
}

func (r *readlineInput) Confirm(ctx context.Context, request policy.Request) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if r.instance == nil {
		if err := r.init(); err != nil {
			return false, err
		}
	}
	r.output.set(r.stderr)
	r.instance.SetPrompt("Allow? [y/N] ")
	defer func() {
		if r.instance != nil {
			r.instance.SetPrompt("you> ")
			r.output.set(r.stdout)
		}
	}()
	fmt.Fprintf(r.stderr, "\n%s\n%s\n", request.Title, request.Details)
	for {
		line, err := r.read(ctx)
		if errors.Is(err, readline.ErrInterrupt) {
			return false, context.Canceled
		}
		if errors.Is(err, io.EOF) {
			r.eof = true
			if request.Kind == "turns" {
				return false, nil
			}
			return false, io.EOF
		}
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				r.inputErr = err
			}
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		default:
			fmt.Fprintln(r.stderr, "Enter y or n. Confirmation replies are not user prompts.")
		}
	}
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
		if strings.HasPrefix(trimmed, "/") && trimmed != "/continue" {
			if handleCommand(conversation, trimmed, stdout, stderr) {
				return 0
			}
			if terminal, ok := input.(*readlineInput); ok {
				if terminal.eof {
					return 0
				}
				if terminal.inputErr != nil {
					return 1
				}
			}
			continue
		}
		ctx, stop := newContext()
		renderer := eventRenderer{stdout: stdout, stderr: stderr, interactive: true}
		var result app.Result
		if trimmed == "/continue" {
			result, err = conversation.Continue(ctx, renderer.Render)
		} else {
			result, err = conversation.Prompt(ctx, line, renderer.Render)
		}
		stop()
		if terminal, ok := input.(*readlineInput); ok {
			if terminal.eof {
				return 0
			}
			if terminal.inputErr != nil {
				fmt.Fprintf(stderr, "error: terminal input: %v\n", terminal.inputErr)
				return 1
			}
		}
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "Canceled. Changes already made are preserved.")
		} else if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		if result.Status == agent.EndTurnLimit {
			printPaused(conversation, stderr)
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
		fmt.Fprintln(stdout, "/help  /quit  /session  /new  /continue  /model [name]\nArrow keys: edit/recall input. Ctrl+C: clear input or cancel work. Ctrl+D: quit on empty input.")
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
	if info.Controls != "" {
		fmt.Fprintf(stdout, "Controls: %s\nTurn batch: %d; can continue: %t\n", info.Controls, info.BatchSize, info.CanContinue)
	}
}
