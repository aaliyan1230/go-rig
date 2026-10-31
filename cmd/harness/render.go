package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

type eventRenderer struct {
	stdout, stderr       io.Writer
	interactive          bool
	printedAssistantText bool
}

func (r *eventRenderer) Render(event agent.AgentEvent) {
	switch e := event.(type) {
	case agent.MessageStart:
		if _, ok := e.Message.(message.AssistantMessage); ok {
			r.printedAssistantText = false
		}
	case agent.MessageUpdate:
		if delta, ok := e.Event.(provider.TextDelta); ok {
			fmt.Fprint(r.stdout, delta.Delta)
			r.printedAssistantText = true
		}
	case agent.MessageEnd:
		if assistant, ok := e.Message.(message.AssistantMessage); ok {
			if !r.printedAssistantText {
				text := assistantText(assistant)
				fmt.Fprint(r.stdout, text)
				r.printedAssistantText = text != ""
			}
			if r.printedAssistantText {
				fmt.Fprintln(r.stdout)
			}
		}
	case agent.ToolExecStart:
		if r.interactive {
			fmt.Fprintf(r.stderr, "tool: %s%s\n", e.ToolName, toolSummary(e.Args))
		} else {
			fmt.Fprintf(r.stderr, "tool: %s %s\n", e.ToolName, string(e.Args))
		}
	case agent.ToolExecEnd:
		status := "complete"
		if e.IsError {
			status = "failed"
		}
		fmt.Fprintf(r.stderr, "tool: %s %s\n", e.ToolName, status)
		if r.interactive {
			var parts []string
			for _, block := range e.Result.Content {
				if text, ok := block.(message.TextContent); ok {
					parts = append(parts, text.Text)
				}
			}
			full := strings.Join(parts, "\n")
			preview := boundedText(full, 1024, 10)
			if preview != "" {
				fmt.Fprintln(r.stderr, preview)
			}
			if preview != full {
				fmt.Fprintln(r.stderr, "[preview truncated]")
			}
		}
	}
}

func toolSummary(args json.RawMessage) string {
	var fields struct {
		Path    string `json:"path"`
		Command string `json:"command"`
	}
	if json.Unmarshal(args, &fields) != nil {
		return ""
	}
	if fields.Path != "" {
		return fmt.Sprintf(" %q", boundedText(fields.Path, 160, 1))
	}
	if fields.Command != "" {
		return fmt.Sprintf(" %q", boundedText(fields.Command, 160, 1))
	}
	return ""
}

func boundedText(text string, maxBytes, maxLines int) string {
	if len(text) > maxBytes {
		text = text[:maxBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		text = strings.Join(lines[:maxLines], "\n")
	}
	return text
}
