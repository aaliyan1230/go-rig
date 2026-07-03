package faux

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

// Client is a fake ModelClient for testing.
// It returns pre-configured AssistantMessage responses,
// simulating streaming by splitting text into small chunks.
type Client struct {
	mu        sync.Mutex
	responses []message.AssistantMessage
	CallCount int
}

var _ provider.ModelClient = (*Client)(nil)

// NewClient creates a faux client with the given responses.
// Each call to Stream() pops the next response from the queue.
func NewClient(responses ...message.AssistantMessage) *Client {
	return &Client{
		responses: append([]message.AssistantMessage{}, responses...),
	}
}

// Stream simulates streaming by chunking text content into small deltas.
func (c *Client) Stream(ctx context.Context, params provider.StreamParams) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent, 1)

	c.mu.Lock()
	c.CallCount++
	var resp message.AssistantMessage
	var hasResp bool
	if len(c.responses) > 0 {
		resp = c.responses[0]
		c.responses = c.responses[1:]
		hasResp = true
	}
	c.mu.Unlock()

	go func() {
		defer close(ch)

		if !hasResp {
			msg := message.AssistantMessage{
				StopReason:   message.StopReasonError,
				ErrorMessage: "no more faux responses queued",
				Timestamp:    time.Now().UnixMilli(),
			}
			send(ch, ctx, provider.StreamError{
				Err:     fmt.Errorf("no more faux responses queued"),
				Message: msg,
			})
			return
		}

		send(ch, ctx, provider.StreamStart{})

		for _, block := range resp.Content {
			if ctx.Err() != nil {
				aborted := resp
				aborted.StopReason = message.StopReasonAborted
				aborted.ErrorMessage = "request was aborted"
				send(ch, ctx, provider.StreamError{
					Err:     ctx.Err(),
					Message: aborted,
				})
				return
			}

			switch b := block.(type) {
			case message.TextContent:
				for _, chunk := range splitText(b.Text) {
					if ctx.Err() != nil {
						aborted := resp
						aborted.StopReason = message.StopReasonAborted
						send(ch, ctx, provider.StreamError{
							Err:     ctx.Err(),
							Message: aborted,
						})
						return
					}
					send(ch, ctx, provider.TextDelta{Delta: chunk})
				}
			case message.ToolCall:
				send(ch, ctx, provider.ToolCallStart{
					Index: 0,
					ID:    b.ID,
					Name:  b.Name,
				})
				argsStr := string(b.Arguments)
				for _, chunk := range splitText(argsStr) {
					send(ch, ctx, provider.ToolCallDelta{
						Index: 0,
						Delta: chunk,
					})
				}
			}
		}

		send(ch, ctx, provider.StreamDone{Message: resp})
	}()

	return ch
}

// splitText splits a string into chunks of 3-8 characters.
func splitText(s string) []string {
	if s == "" {
		return nil
	}
	var chunks []string
	i := 0
	for i < len(s) {
		size := 3 + rand.Intn(6) // 3 to 8
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[i:end])
		i = end
	}
	return chunks
}

func send(ch chan<- provider.StreamEvent, ctx context.Context, evt provider.StreamEvent) {
	select {
	case ch <- evt:
	case <-ctx.Done():
	}
}

// TextMessage is a convenience constructor for a simple text response.
func TextMessage(text string) message.AssistantMessage {
	return message.AssistantMessage{
		Content: []message.ContentBlock{
			message.TextContent{Text: text},
		},
		Model:      "faux-1",
		Provider:   "faux",
		StopReason: message.StopReasonStop,
		Timestamp:  time.Now().UnixMilli(),
	}
}

// ToolCallMessage is a convenience constructor for a tool call response.
func ToolCallMessage(toolName, toolID string, args map[string]any) message.AssistantMessage {
	argsJSON, _ := json.Marshal(args)
	return message.AssistantMessage{
		Content: []message.ContentBlock{
			message.ToolCall{
				ID:        toolID,
				Name:      toolName,
				Arguments: argsJSON,
			},
		},
		Model:      "faux-1",
		Provider:   "faux",
		StopReason: message.StopReasonToolUse,
		Timestamp:  time.Now().UnixMilli(),
	}
}
