// Package policy contains host-side decisions, independent of model reasoning.
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/message"
)

type Request struct{ Kind, Title, Details string }
type Confirm func(context.Context, Request) (bool, error)
type Answer struct {
	Allowed bool
	Err     error
}
type Pending struct {
	Context context.Context
	Request Request
	Reply   chan Answer
}

// Broker transfers decisions from agent callbacks to the conversation's single
// UI owner. Buffered replies let cancellation finish without stranding a sender.
type Broker struct{ requests chan Pending }

func NewBroker() *Broker                   { return &Broker{requests: make(chan Pending)} }
func (b *Broker) Requests() <-chan Pending { return b.requests }
func (b *Broker) Ask(ctx context.Context, r Request) (bool, error) {
	p := Pending{Context: ctx, Request: r, Reply: make(chan Answer, 1)}
	select {
	case b.requests <- p:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	select {
	case a := <-p.Reply:
		return a.Allowed, a.Err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

type Controls struct {
	Tools          *string
	ExcludeTools   string
	NoTools        bool
	Approval       string
	PathScope      string
	ProjectTrust   string
	NoContextFiles bool
}

func Defaults() Controls { return Controls{Approval: "none", PathScope: "all", ProjectTrust: "auto"} }
func (c Controls) Validate() error {
	if c.Approval != "none" && c.Approval != "ask" {
		return fmt.Errorf("--approval must be none or ask")
	}
	if c.PathScope != "all" && c.PathScope != "workspace" {
		return fmt.Errorf("--path-scope must be all or workspace")
	}
	switch c.ProjectTrust {
	case "auto", "ask", "yes", "no":
	default:
		return fmt.Errorf("--project-trust must be auto, ask, yes, or no")
	}
	if c.NoTools && c.Tools != nil {
		return fmt.Errorf("--no-tools and --tools cannot be combined")
	}
	return nil
}

// Select operates on the registered catalog, so schemas and executors use the
// same filtered list. Defaults are supplied separately for internal configuration.
func Select(catalog, defaults []agent.Tool, c Controls) ([]agent.Tool, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, t := range catalog {
		known[t.ToolName()] = true
	}
	parse := func(value string) (map[string]bool, error) {
		set := map[string]bool{}
		if strings.TrimSpace(value) == "" {
			return set, nil
		}
		for _, name := range strings.Split(value, ",") {
			name = strings.TrimSpace(name)
			if !known[name] {
				return nil, fmt.Errorf("unknown tool %q", name)
			}
			set[name] = true
		}
		return set, nil
	}
	enabled := map[string]bool{}
	for _, t := range defaults {
		enabled[t.ToolName()] = true
	}
	if c.Tools != nil {
		var err error
		enabled, err = parse(*c.Tools)
		if err != nil {
			return nil, err
		}
	}
	excluded, err := parse(c.ExcludeTools)
	if err != nil {
		return nil, err
	}
	var selected []agent.Tool
	for _, t := range catalog {
		if !c.NoTools && enabled[t.ToolName()] && !excluded[t.ToolName()] {
			selected = append(selected, t)
		}
	}
	return selected, nil
}

// ReadOnlyTool is an optional classification supplied by the registered tool.
// It is a host policy hint, not isolation from untrusted extension code.
type ReadOnlyTool interface{ ReadOnly() bool }

func ApprovalGate(mode string, ask Confirm, catalog ...agent.Tool) func(context.Context, message.ToolCall) error {
	readOnly := map[string]bool{}
	if len(catalog) == 0 {
		readOnly["read"] = true
	}
	for _, tool := range catalog {
		if hint, ok := tool.(ReadOnlyTool); ok && hint.ReadOnly() {
			readOnly[tool.ToolName()] = true
		}
	}
	return func(ctx context.Context, call message.ToolCall) error {
		if mode == "none" || readOnly[call.Name] {
			return nil
		}
		if !json.Valid(call.Arguments) {
			return fmt.Errorf("invalid tool arguments")
		}
		var object any
		if err := json.Unmarshal(call.Arguments, &object); err != nil {
			return err
		}
		data, _ := json.MarshalIndent(object, "", "  ")
		if ask == nil {
			return fmt.Errorf("operation denied: approval requires terminal input")
		}
		allowed, err := ask(ctx, Request{Kind: "tool", Title: fmt.Sprintf("Allow tool %q?", call.Name), Details: string(data)})
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("operation denied: approval was not granted")
		}
		return nil
	}
}

func (c Controls) Summary(tools []agent.Tool) string {
	names := make([]string, 0, len(tools))
	bash := false
	for _, t := range tools {
		names = append(names, t.ToolName())
		bash = bash || t.ToolName() == "bash"
	}
	list := strings.Join(names, ",")
	if list == "" {
		list = "none"
	}
	s := fmt.Sprintf("Tools: %s; approval: %s; filesystem scope: %s; project instructions: %s", list, c.Approval, c.PathScope, c.ProjectTrust)
	if c.NoContextFiles {
		s += " (disabled)"
	}
	if c.PathScope == "workspace" && bash {
		s += ". Bash is NOT confined by filesystem scope; shell approval follows --approval."
	}
	return s
}
