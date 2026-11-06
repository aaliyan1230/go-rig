package agent

import (
	"encoding/json"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

// AgentEvent is a sealed interface for events emitted by the agent loop.
type AgentEvent interface {
	agentEvent()
}

// Agent lifecycle

type AgentStart struct{}

func (AgentStart) agentEvent() {}

type AgentEnd struct {
	Messages []message.AgentMessage
	Err      error
	Reason   EndReason
	Turns    int
}

type EndReason string

const (
	EndCompleted EndReason = "completed"
	EndTurnLimit EndReason = "turn_limit"
	EndCanceled  EndReason = "canceled"
	EndError     EndReason = "error"
)

func (AgentEnd) agentEvent() {}

// Turn lifecycle

type TurnStart struct{}

func (TurnStart) agentEvent() {}

type TurnEnd struct {
	Message     message.AssistantMessage
	ToolResults []message.ToolResultMessage
}

func (TurnEnd) agentEvent() {}

// Message lifecycle

type MessageStart struct {
	Message message.AgentMessage
}

func (MessageStart) agentEvent() {}

type MessageUpdate struct {
	Message message.AgentMessage
	Event   provider.StreamEvent
}

func (MessageUpdate) agentEvent() {}

type MessageEnd struct {
	Message message.AgentMessage
}

func (MessageEnd) agentEvent() {}

// Tool execution lifecycle

type ToolExecStart struct {
	ToolCallID string
	ToolName   string
	Args       json.RawMessage
}

func (ToolExecStart) agentEvent() {}

type ToolExecUpdate struct {
	ToolCallID string
	ToolName   string
	Result     ToolResult
}

func (ToolExecUpdate) agentEvent() {}

type ToolExecEnd struct {
	ToolCallID string
	ToolName   string
	Result     ToolResult
	IsError    bool
}

func (ToolExecEnd) agentEvent() {}
