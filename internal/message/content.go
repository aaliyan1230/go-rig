package message

import "encoding/json"

// ContentBlock is a sealed interface for content within messages.
// Only types in this package can implement it.
type ContentBlock interface {
	contentType() string
	contentBlock()
}

type TextContent struct {
	Text string `json:"text"`
}

func (TextContent) contentType() string { return "text" }
func (TextContent) contentBlock()       {}

type ImageContent struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

func (ImageContent) contentType() string { return "image" }
func (ImageContent) contentBlock()       {}

type ThinkingContent struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
	Redacted  bool   `json:"redacted,omitempty"`
}

func (ThinkingContent) contentType() string { return "thinking" }
func (ThinkingContent) contentBlock()       {}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (ToolCall) contentType() string { return "toolCall" }
func (ToolCall) contentBlock()       {}
