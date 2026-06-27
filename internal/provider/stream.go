package provider

import "github.com/aaliyan1230/go-rig/internal/message"

// StreamEvent is a sealed interface for events emitted by ModelClient.Stream.
type StreamEvent interface {
	streamEvent()
}

type StreamStart struct{}

func (StreamStart) streamEvent() {}

type TextDelta struct {
	Delta string
}

func (TextDelta) streamEvent() {}

type ToolCallStart struct {
	Index int
	ID    string
	Name  string
}

func (ToolCallStart) streamEvent() {}

type ToolCallDelta struct {
	Index int
	Delta string
}

func (ToolCallDelta) streamEvent() {}

type StreamDone struct {
	Message message.AssistantMessage
}

func (StreamDone) streamEvent() {}

type StreamError struct {
	Err     error
	Message message.AssistantMessage
}

func (StreamError) streamEvent() {}
