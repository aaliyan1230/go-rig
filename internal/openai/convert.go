package openai

import (
	"encoding/json"
	"strings"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

// apiRequest is the OpenAI Chat Completions API request body.
type apiRequest struct {
	Model    string       `json:"model"`
	Messages []apiMessage `json:"messages"`
	Tools    []apiTool    `json:"tools,omitempty"`
	Stream   bool         `json:"stream"`
}

// apiMessage represents a message in the OpenAI API format.
// Content can be a string or an array of content parts (for images).
type apiMessage struct {
	Role       string          `json:"role"`
	Content    any             `json:"content,omitempty"`
	ToolCalls  []apiToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type apiContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *apiImageURL  `json:"image_url,omitempty"`
}

type apiImageURL struct {
	URL string `json:"url"`
}

type apiToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function apiFunction `json:"function"`
}

type apiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type apiTool struct {
	Type     string         `json:"type"`
	Function apiFunctionDef `json:"function"`
}

type apiFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// buildRequest converts StreamParams into an OpenAI Chat Completions request.
func buildRequest(params provider.StreamParams) apiRequest {
	return apiRequest{
		Model:    params.Model,
		Messages: convertMessages(params.SystemPrompt, params.Messages),
		Tools:    convertTools(params.Tools),
		Stream:   true,
	}
}

// convertMessages converts internal Message types to OpenAI API format.
// The system prompt becomes a separate system message prepended to the list.
func convertMessages(systemPrompt string, msgs []message.Message) []apiMessage {
	var result []apiMessage

	if systemPrompt != "" {
		result = append(result, apiMessage{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	for _, msg := range msgs {
		switch m := msg.(type) {
		case message.UserMessage:
			result = append(result, convertUserMessage(m))
		case message.AssistantMessage:
			result = append(result, convertAssistantMessage(m))
		case message.ToolResultMessage:
			result = append(result, convertToolResultMessage(m))
		}
	}

	return result
}

func convertUserMessage(m message.UserMessage) apiMessage {
	hasImages := false
	for _, block := range m.Content {
		if _, ok := block.(message.ImageContent); ok {
			hasImages = true
			break
		}
	}

	if !hasImages {
		var texts []string
		for _, block := range m.Content {
			if tc, ok := block.(message.TextContent); ok {
				texts = append(texts, tc.Text)
			}
		}
		return apiMessage{
			Role:    "user",
			Content: strings.Join(texts, "\n"),
		}
	}

	var parts []apiContentPart
	for _, block := range m.Content {
		switch b := block.(type) {
		case message.TextContent:
			parts = append(parts, apiContentPart{
				Type: "text",
				Text: b.Text,
			})
		case message.ImageContent:
			parts = append(parts, apiContentPart{
				Type: "image_url",
				ImageURL: &apiImageURL{
					URL: "data:" + b.MimeType + ";base64," + b.Data,
				},
			})
		}
	}

	return apiMessage{
		Role:    "user",
		Content: parts,
	}
}

func convertAssistantMessage(m message.AssistantMessage) apiMessage {
	var textParts []string
	var toolCalls []apiToolCall

	for _, block := range m.Content {
		switch b := block.(type) {
		case message.TextContent:
			textParts = append(textParts, b.Text)
		case message.ToolCall:
			toolCalls = append(toolCalls, apiToolCall{
				ID:   b.ID,
				Type: "function",
				Function: apiFunction{
					Name:      b.Name,
					Arguments: string(b.Arguments),
				},
			})
		}
	}

	msg := apiMessage{
		Role: "assistant",
	}
	if len(textParts) > 0 {
		msg.Content = strings.Join(textParts, "\n")
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	return msg
}

func convertToolResultMessage(m message.ToolResultMessage) apiMessage {
	var texts []string
	for _, block := range m.Content {
		if tc, ok := block.(message.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}

	return apiMessage{
		Role:       "tool",
		Content:    strings.Join(texts, "\n"),
		ToolCallID: m.ToolCallID,
	}
}

// convertTools converts internal ToolSpec types to OpenAI API format.
func convertTools(tools []provider.ToolSpec) []apiTool {
	if len(tools) == 0 {
		return nil
	}

	result := make([]apiTool, len(tools))
	for i, t := range tools {
		result[i] = apiTool{
			Type: "function",
			Function: apiFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		}
	}
	return result
}
