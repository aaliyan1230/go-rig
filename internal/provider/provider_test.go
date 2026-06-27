package provider

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestToolSpecJSON(t *testing.T) {
	spec := ToolSpec{
		Name:        "read",
		Description: "Read a file from disk",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ToolSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Name != spec.Name {
		t.Errorf("Name: got %q, want %q", got.Name, spec.Name)
	}
	if got.Description != spec.Description {
		t.Errorf("Description: got %q, want %q", got.Description, spec.Description)
	}

	var origParams, gotParams map[string]any
	json.Unmarshal(spec.Parameters, &origParams)
	json.Unmarshal(got.Parameters, &gotParams)
	if !reflect.DeepEqual(origParams, gotParams) {
		t.Errorf("Parameters mismatch:\n  got:  %s\n  want: %s", got.Parameters, spec.Parameters)
	}
}

func TestStreamEventTypeSwitch(t *testing.T) {
	events := []StreamEvent{
		StreamStart{},
		TextDelta{Delta: "hello"},
		ToolCallStart{Index: 0, ID: "call_1", Name: "read"},
		ToolCallDelta{Index: 0, Delta: `{"path":"`},
		StreamDone{},
		StreamError{Err: nil},
	}

	for i, event := range events {
		switch event.(type) {
		case StreamStart:
		case TextDelta:
		case ToolCallStart:
		case ToolCallDelta:
		case StreamDone:
		case StreamError:
		default:
			t.Errorf("events[%d]: unhandled type %T", i, event)
		}
	}
}
