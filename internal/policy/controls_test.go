package policy

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/tools"
)

func TestSelectFiltersCatalogAndValidatesNames(t *testing.T) {
	e := env.NewLocalEnv(t.TempDir())
	catalog := []agent.Tool{tools.NewReadTool(e), tools.NewWriteTool(e), tools.NewEditTool(e), tools.NewBashTool(e)}
	list := "bash,read,edit,read"
	for _, tc := range []struct {
		name      string
		configure func(*Controls)
		want      []string
		bad       bool
	}{
		{"defaults", func(*Controls) {}, []string{"read", "edit"}, false},
		{"allow and exclude", func(c *Controls) { c.Tools = &list; c.ExcludeTools = "edit" }, []string{"read", "bash"}, false},
		{"none", func(c *Controls) { c.NoTools = true }, nil, false},
		{"unknown", func(c *Controls) { v := "reed"; c.Tools = &v }, nil, true},
		{"unknown deny", func(c *Controls) { c.ExcludeTools = "nope" }, nil, true},
		{"conflict", func(c *Controls) { c.NoTools = true; c.Tools = &list }, nil, true},
		{"invalid policy", func(c *Controls) { c.Approval = "maybe" }, nil, true},
		{"invalid paths", func(c *Controls) { c.PathScope = "sandbox" }, nil, true},
		{"invalid trust", func(c *Controls) { c.ProjectTrust = "maybe" }, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Defaults()
			tc.configure(&c)
			selected, err := Select(catalog, []agent.Tool{catalog[0], catalog[2]}, c)
			if (err != nil) != tc.bad {
				t.Fatalf("got %v", err)
			}
			if tc.bad {
				return
			}
			var names []string
			for _, tool := range selected {
				names = append(names, tool.ToolName())
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("got %v want %v", names, tc.want)
			}
			if len(agent.ToolSpecs(selected)) != len(selected) {
				t.Fatal("schema/executor mismatch")
			}
		})
	}
}

func TestApprovalModesAndUnknownTools(t *testing.T) {
	call := message.ToolCall{Name: "write", ID: "call", Arguments: json.RawMessage(`{"path":"file.go","content":"proposed\ncontent"}`)}
	asked := 0
	ask := func(_ context.Context, r Request) (bool, error) {
		asked++
		if !strings.Contains(r.Details, "proposed") || !strings.Contains(r.Details, "file.go") {
			t.Error(r)
		}
		return true, nil
	}
	if ApprovalGate("ask", ask)(context.Background(), call) != nil || asked != 1 {
		t.Fatal("approval not requested")
	}
	call.Name = "future-tool"
	if ApprovalGate("ask", ask)(context.Background(), call) != nil || asked != 2 {
		t.Fatal("unknown tool bypassed approval")
	}
	call.Name = "read"
	if ApprovalGate("ask", nil)(context.Background(), call) != nil {
		t.Fatal("read should not need approval")
	}
	call.Name = "bash"
	if ApprovalGate("none", nil)(context.Background(), call) != nil {
		t.Fatal("autonomous mode blocked")
	}
	if ApprovalGate("ask", nil)(context.Background(), call) == nil {
		t.Fatal("missing UI must deny")
	}
	deny := func(context.Context, Request) (bool, error) { return false, nil }
	if ApprovalGate("ask", deny)(context.Background(), call) == nil {
		t.Fatal("denied tool allowed")
	}
	cancel := func(context.Context, Request) (bool, error) { return false, context.Canceled }
	if !errors.Is(ApprovalGate("ask", cancel)(context.Background(), call), context.Canceled) {
		t.Fatal("cancellation swallowed")
	}
}

func TestBrokerCancellationWithoutUI(t *testing.T) {
	b := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Ask(ctx, Request{Kind: "turns"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type classifiedReader struct{ agent.Tool }

func (classifiedReader) ToolName() string { return "inspect" }
func (classifiedReader) ReadOnly() bool   { return true }

func TestRegisteredReadOnlyClassification(t *testing.T) {
	tool := classifiedReader{tools.NewReadTool(env.NewLocalEnv(t.TempDir()))}
	call := message.ToolCall{Name: "inspect", Arguments: json.RawMessage(`{}`)}
	if err := ApprovalGate("ask", nil, tool)(context.Background(), call); err != nil {
		t.Fatal(err)
	}
}
