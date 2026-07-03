package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
	"github.com/aaliyan1230/go-rig/internal/sse"
)

const defaultBaseURL = "https://api.openai.com"

// Client implements provider.ModelClient for OpenAI-compatible APIs.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

var _ provider.ModelClient = (*Client)(nil)

// NewClient creates a new OpenAI-compatible client.
// baseURL defaults to "https://api.openai.com" if empty.
func NewClient(apiKey, baseURL string) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}, nil
}

// Stream sends a streaming Chat Completions request and returns a channel of StreamEvents.
// The channel is closed when the stream ends (successfully or with an error).
func (c *Client) Stream(ctx context.Context, params provider.StreamParams) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent, 1)

	go func() {
		defer close(ch)

		req, state, err := c.prepareRequest(ctx, params)
		if err != nil {
			c.sendError(ch, ctx, err, state)
			return
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			c.sendError(ch, ctx, fmt.Errorf("request failed: %w", err), state)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			c.handleHTTPError(ch, ctx, resp, state)
			return
		}

		c.send(ch, ctx, provider.StreamStart{})
		c.processStream(ch, ctx, resp.Body, state)
	}()

	return ch
}

func (c *Client) prepareRequest(ctx context.Context, params provider.StreamParams) (*http.Request, *streamState, error) {
	state := newStreamState(params.Model, "openai")

	apiReq := buildRequest(params)
	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, state, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, state, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	return req, state, nil
}

func (c *Client) handleHTTPError(ch chan<- provider.StreamEvent, ctx context.Context, resp *http.Response, state *streamState) {
	body, _ := io.ReadAll(resp.Body)

	var errMsg string
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
		errMsg = apiErr.Error.Message
	} else {
		errMsg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	msg := state.buildMessage()
	msg.StopReason = message.StopReasonError
	msg.ErrorMessage = errMsg
	c.send(ch, ctx, provider.StreamError{Err: fmt.Errorf("%s", errMsg), Message: msg})
}

func (c *Client) processStream(ch chan<- provider.StreamEvent, ctx context.Context, body io.Reader, state *streamState) {
	events := sse.Decode(ctx, body)
	sawDone := false

	for evt := range events {
		if evt.Data == "[DONE]" {
			sawDone = true
			break
		}

		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(evt.Data), &chunk); err != nil {
			msg := state.buildMessage()
			msg.StopReason = message.StopReasonError
			msg.ErrorMessage = fmt.Sprintf("failed to parse chunk: %v", err)
			c.send(ch, ctx, provider.StreamError{
				Err:     fmt.Errorf("failed to parse chunk: %w", err),
				Message: msg,
			})
			return
		}

		for _, streamEvt := range state.processChunk(chunk) {
			c.send(ch, ctx, streamEvt)
		}
	}

	msg := state.buildMessage()
	if !sawDone {
		if ctx.Err() != nil {
			msg.StopReason = message.StopReasonAborted
			msg.ErrorMessage = "request was aborted"
			c.send(ch, ctx, provider.StreamError{
				Err:     ctx.Err(),
				Message: msg,
			})
			return
		}
		msg.StopReason = message.StopReasonError
		msg.ErrorMessage = "stream ended without [DONE]"
		c.send(ch, ctx, provider.StreamError{
			Err:     fmt.Errorf("stream ended without [DONE]"),
			Message: msg,
		})
		return
	}

	c.send(ch, ctx, provider.StreamDone{Message: msg})
}

// send writes an event to the channel, respecting context cancellation.
func (c *Client) send(ch chan<- provider.StreamEvent, ctx context.Context, evt provider.StreamEvent) {
	select {
	case ch <- evt:
	case <-ctx.Done():
	}
}

func (c *Client) sendError(ch chan<- provider.StreamEvent, ctx context.Context, err error, state *streamState) {
	msg := state.buildMessage()
	msg.StopReason = message.StopReasonError
	msg.ErrorMessage = err.Error()
	c.send(ch, ctx, provider.StreamError{Err: err, Message: msg})
}
