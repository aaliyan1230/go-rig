package openai

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

// chatCompletionChunk is the JSON structure of a streaming chunk
// from the OpenAI Chat Completions API.
type chatCompletionChunk struct {
	ID      string   `json:"id"`
	Choices []choice `json:"choices"`
	Model   string   `json:"model"`
	Usage   *chunkUsage `json:"usage,omitempty"`
}

type choice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type delta struct {
	Role      string          `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"`
	ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    int            `json:"index"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function *functionDelta `json:"function,omitempty"`
}

type functionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chunkUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// toolCallAccum tracks a tool call being assembled from streaming deltas.
type toolCallAccum struct {
	id   string
	name string
	args strings.Builder
}

// streamState tracks the accumulating assistant message during streaming.
type streamState struct {
	textParts []string
	toolCalls map[int]*toolCallAccum
	usage     message.Usage
	stopReason message.StopReason
	model      string
	provider   string
}

func newStreamState(model, prov string) *streamState {
	return &streamState{
		toolCalls:  make(map[int]*toolCallAccum),
		stopReason: message.StopReasonStop,
		model:      model,
		provider:   prov,
	}
}

// processChunk handles a single streaming chunk and returns zero or more
// StreamEvents to send to the consumer.
func (s *streamState) processChunk(chunk chatCompletionChunk) []provider.StreamEvent {
	if chunk.Model != "" {
		s.model = chunk.Model
	}

	var events []provider.StreamEvent

	for _, c := range chunk.Choices {
		if c.Delta.Content != nil && *c.Delta.Content != "" {
			s.textParts = append(s.textParts, *c.Delta.Content)
			events = append(events, provider.TextDelta{Delta: *c.Delta.Content})
		}

		for _, tc := range c.Delta.ToolCalls {
			accum, exists := s.toolCalls[tc.Index]
			if !exists {
				accum = &toolCallAccum{}
				s.toolCalls[tc.Index] = accum
			}

			if tc.ID != "" {
				accum.id = tc.ID
			}
			if tc.Function != nil && tc.Function.Name != "" {
				accum.name = tc.Function.Name
				events = append(events, provider.ToolCallStart{
					Index: tc.Index,
					ID:    accum.id,
					Name:  accum.name,
				})
			}
			if tc.Function != nil && tc.Function.Arguments != "" {
				accum.args.WriteString(tc.Function.Arguments)
				events = append(events, provider.ToolCallDelta{
					Index: tc.Index,
					Delta: tc.Function.Arguments,
				})
			}
		}

		if c.FinishReason != nil {
			s.stopReason = mapFinishReason(*c.FinishReason)
		}
	}

	if chunk.Usage != nil {
		s.usage.Input = chunk.Usage.PromptTokens
		s.usage.Output = chunk.Usage.CompletionTokens
	}

	return events
}

// buildMessage assembles the final AssistantMessage from accumulated state.
func (s *streamState) buildMessage() message.AssistantMessage {
	var content []message.ContentBlock

	if len(s.textParts) > 0 {
		content = append(content, message.TextContent{
			Text: strings.Join(s.textParts, ""),
		})
	}

	indices := sortedKeys(s.toolCalls)
	for _, idx := range indices {
		tc := s.toolCalls[idx]
		content = append(content, message.ToolCall{
			ID:        tc.id,
			Name:      tc.name,
			Arguments: json.RawMessage(tc.args.String()),
		})
	}

	return message.AssistantMessage{
		Content:    content,
		Model:      s.model,
		Provider:   s.provider,
		Usage:      s.usage,
		StopReason: s.stopReason,
		Timestamp:  time.Now().UnixMilli(),
	}
}

func mapFinishReason(reason string) message.StopReason {
	switch reason {
	case "stop":
		return message.StopReasonStop
	case "length":
		return message.StopReasonLength
	case "tool_calls":
		return message.StopReasonToolUse
	default:
		return message.StopReasonError
	}
}

// sortedKeys returns the keys of a map in ascending order.
func sortedKeys(m map[int]*toolCallAccum) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
