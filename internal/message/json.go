package message

import (
	"encoding/json"
	"fmt"
)

// contentBlockEnvelope is the JSON wire format for content blocks.
type contentBlockEnvelope struct {
	Type string `json:"type"`
}

// MarshalContentBlock serializes a ContentBlock to JSON with a "type" discriminator.
func MarshalContentBlock(cb ContentBlock) ([]byte, error) {
	type wrapper struct {
		Type string `json:"type"`
	}

	switch v := cb.(type) {
	case TextContent:
		return json.Marshal(struct {
			wrapper
			TextContent
		}{wrapper{"text"}, v})
	case ImageContent:
		return json.Marshal(struct {
			wrapper
			ImageContent
		}{wrapper{"image"}, v})
	case ThinkingContent:
		return json.Marshal(struct {
			wrapper
			ThinkingContent
		}{wrapper{"thinking"}, v})
	case ToolCall:
		return json.Marshal(struct {
			wrapper
			ToolCall
		}{wrapper{"toolCall"}, v})
	default:
		return nil, fmt.Errorf("unknown content block type: %T", cb)
	}
}

// UnmarshalContentBlock deserializes JSON into a ContentBlock, dispatching on "type".
func UnmarshalContentBlock(data []byte) (ContentBlock, error) {
	var env contentBlockEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("unmarshal content block type: %w", err)
	}

	switch env.Type {
	case "text":
		var v TextContent
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return v, nil
	case "image":
		var v ImageContent
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return v, nil
	case "thinking":
		var v ThinkingContent
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return v, nil
	case "toolCall":
		var v ToolCall
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unknown content block type: %q", env.Type)
	}
}

// marshalContentBlocks serializes a slice of ContentBlocks to a JSON array.
func marshalContentBlocks(blocks []ContentBlock) (json.RawMessage, error) {
	arr := make([]json.RawMessage, len(blocks))
	for i, b := range blocks {
		data, err := MarshalContentBlock(b)
		if err != nil {
			return nil, err
		}
		arr[i] = data
	}
	return json.Marshal(arr)
}

// unmarshalContentBlocks deserializes a JSON array into a slice of ContentBlocks.
func unmarshalContentBlocks(data json.RawMessage) ([]ContentBlock, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, err
	}
	blocks := make([]ContentBlock, len(raws))
	for i, raw := range raws {
		b, err := UnmarshalContentBlock(raw)
		if err != nil {
			return nil, err
		}
		blocks[i] = b
	}
	return blocks, nil
}

// messageEnvelope is the JSON wire format for messages.
type messageEnvelope struct {
	Role string `json:"role"`
}

// MarshalMessage serializes a Message to JSON with a "role" discriminator.
func MarshalMessage(m Message) ([]byte, error) {
	type wrapper struct {
		Role string `json:"role"`
	}

	switch v := m.(type) {
	case UserMessage:
		content, err := marshalContentBlocks(v.Content)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Timestamp int64           `json:"timestamp"`
		}{"user", content, v.Timestamp})

	case AssistantMessage:
		content, err := marshalContentBlocks(v.Content)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Role         string          `json:"role"`
			Content      json.RawMessage `json:"content"`
			Model        string          `json:"model"`
			Provider     string          `json:"provider"`
			Usage        Usage           `json:"usage"`
			StopReason   StopReason      `json:"stopReason"`
			ErrorMessage string          `json:"errorMessage,omitempty"`
			Timestamp    int64           `json:"timestamp"`
		}{"assistant", content, v.Model, v.Provider, v.Usage, v.StopReason, v.ErrorMessage, v.Timestamp})

	case ToolResultMessage:
		content, err := marshalContentBlocks(v.Content)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Role       string          `json:"role"`
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Content    json.RawMessage `json:"content"`
			IsError    bool            `json:"isError"`
			Timestamp  int64           `json:"timestamp"`
		}{"toolResult", v.ToolCallID, v.ToolName, content, v.IsError, v.Timestamp})

	default:
		return nil, fmt.Errorf("unknown message type: %T", m)
	}
}

// UnmarshalMessage deserializes JSON into a Message, dispatching on "role".
func UnmarshalMessage(data []byte) (Message, error) {
	var env messageEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("unmarshal message role: %w", err)
	}

	switch env.Role {
	case "user":
		var raw struct {
			Content   json.RawMessage `json:"content"`
			Timestamp int64           `json:"timestamp"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		content, err := unmarshalContentBlocks(raw.Content)
		if err != nil {
			return nil, err
		}
		return UserMessage{Content: content, Timestamp: raw.Timestamp}, nil

	case "assistant":
		var raw struct {
			Content      json.RawMessage `json:"content"`
			Model        string          `json:"model"`
			Provider     string          `json:"provider"`
			Usage        Usage           `json:"usage"`
			StopReason   StopReason      `json:"stopReason"`
			ErrorMessage string          `json:"errorMessage"`
			Timestamp    int64           `json:"timestamp"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		content, err := unmarshalContentBlocks(raw.Content)
		if err != nil {
			return nil, err
		}
		return AssistantMessage{
			Content:      content,
			Model:        raw.Model,
			Provider:     raw.Provider,
			Usage:        raw.Usage,
			StopReason:   raw.StopReason,
			ErrorMessage: raw.ErrorMessage,
			Timestamp:    raw.Timestamp,
		}, nil

	case "toolResult":
		var raw struct {
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Content    json.RawMessage `json:"content"`
			IsError    bool            `json:"isError"`
			Timestamp  int64           `json:"timestamp"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		content, err := unmarshalContentBlocks(raw.Content)
		if err != nil {
			return nil, err
		}
		return ToolResultMessage{
			ToolCallID: raw.ToolCallID,
			ToolName:   raw.ToolName,
			Content:    content,
			IsError:    raw.IsError,
			Timestamp:  raw.Timestamp,
		}, nil

	default:
		return nil, fmt.Errorf("unknown message role: %q", env.Role)
	}
}
