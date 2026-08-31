package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

var (
	ErrNoPrompts = errors.New("agent requires at least one prompt")
	ErrMaxTurns  = errors.New("maximum agent turns reached")
)

// AgentContext is the provider-independent state available to one run.
// Messages are prior context; prompts passed to Run are appended to it.
type AgentContext struct {
	SystemPrompt string
	Messages     []message.AgentMessage
	Tools        []Tool
}

// RunConfig controls provider requests made by the agent loop.
type RunConfig struct {
	Model     string
	MaxTokens int
	MaxTurns  int
}

// Run starts a single agent run. Events are emitted in lifecycle order and the
// returned channel is closed after AgentEnd. Cancelling ctx stops new work.
func Run(
	ctx context.Context,
	client provider.ModelClient,
	prompts []message.AgentMessage,
	initial AgentContext,
	cfg RunConfig,
) <-chan AgentEvent {
	ch := make(chan AgentEvent, 1)
	go func() {
		defer close(ch)
		run(ctx, client, prompts, initial, cfg, ch)
	}()
	return ch
}

func run(
	ctx context.Context,
	client provider.ModelClient,
	prompts []message.AgentMessage,
	initial AgentContext,
	cfg RunConfig,
	ch chan<- AgentEvent,
) {
	working := AgentContext{
		SystemPrompt: initial.SystemPrompt,
		Messages:     append([]message.AgentMessage(nil), initial.Messages...),
		Tools:        append([]Tool(nil), initial.Tools...),
	}
	newMessages := append([]message.AgentMessage(nil), prompts...)

	if !emit(ctx, ch, AgentStart{}) {
		emitTerminal(ch, AgentEnd{Messages: newMessages, Err: ctx.Err()})
		return
	}
	if len(prompts) == 0 {
		emitTerminal(ch, AgentEnd{Messages: newMessages, Err: ErrNoPrompts})
		return
	}
	if cfg.MaxTurns <= 0 {
		emitTerminal(ch, AgentEnd{Messages: newMessages, Err: fmt.Errorf("%w: must be greater than zero", ErrMaxTurns)})
		return
	}

	turns := 0
	promptsAdded := false
	for {
		if err := ctx.Err(); err != nil {
			emitTerminal(ch, AgentEnd{Messages: newMessages, Err: err})
			return
		}
		if turns >= cfg.MaxTurns {
			emitTerminal(ch, AgentEnd{Messages: newMessages, Err: ErrMaxTurns})
			return
		}

		if !emit(ctx, ch, TurnStart{}) {
			emitTerminal(ch, AgentEnd{Messages: newMessages, Err: ctx.Err()})
			return
		}
		if !promptsAdded {
			for _, prompt := range prompts {
				working.Messages = append(working.Messages, prompt)
				if !emit(ctx, ch, MessageStart{Message: prompt}) || !emit(ctx, ch, MessageEnd{Message: prompt}) {
					emitTerminal(ch, AgentEnd{Messages: newMessages, Err: ctx.Err()})
					return
				}
			}
			promptsAdded = true
		}
		turns++

		assistant, streamErr := streamAssistant(ctx, ch, client, working, cfg)
		working.Messages = append(working.Messages, assistant)
		newMessages = append(newMessages, assistant)
		if streamErr != nil {
			emit(ctx, ch, TurnEnd{Message: assistant})
			emitTerminal(ch, AgentEnd{Messages: newMessages, Err: streamErr})
			return
		}

		toolResults := make([]message.ToolResultMessage, 0)
		for _, call := range toolCalls(assistant) {
			result := executeToolCall(ctx, ch, working.Tools, call)
			toolResults = append(toolResults, result)
			working.Messages = append(working.Messages, result)
			newMessages = append(newMessages, result)
			if ctx.Err() != nil {
				break
			}
		}

		if !emit(ctx, ch, TurnEnd{Message: assistant, ToolResults: toolResults}) {
			emitTerminal(ch, AgentEnd{Messages: newMessages, Err: ctx.Err()})
			return
		}
		if err := ctx.Err(); err != nil {
			emitTerminal(ch, AgentEnd{Messages: newMessages, Err: err})
			return
		}
		if len(toolResults) == 0 {
			emitTerminal(ch, AgentEnd{Messages: newMessages})
			return
		}
	}
}

func streamAssistant(
	ctx context.Context,
	ch chan<- AgentEvent,
	client provider.ModelClient,
	agentContext AgentContext,
	cfg RunConfig,
) (message.AssistantMessage, error) {
	params := provider.StreamParams{
		SystemPrompt: agentContext.SystemPrompt,
		Messages:     message.ConvertToLLM(agentContext.Messages),
		Tools:        ToolSpecs(agentContext.Tools),
		Model:        cfg.Model,
		MaxTokens:    cfg.MaxTokens,
	}

	partial := message.AssistantMessage{
		Model:     cfg.Model,
		Timestamp: time.Now().UnixMilli(),
	}
	started := false
	toolBlocks := make(map[int]int)
	start := func(msg message.AssistantMessage) bool {
		if started {
			return true
		}
		partial = msg
		started = true
		return emit(ctx, ch, MessageStart{Message: cloneAssistantMessage(partial)})
	}
	finish := func(msg message.AssistantMessage, err error) (message.AssistantMessage, error) {
		if !start(msg) {
			return msg, ctx.Err()
		}
		if !emit(ctx, ch, MessageEnd{Message: msg}) {
			return msg, ctx.Err()
		}
		return msg, err
	}

	events := client.Stream(ctx, params)
	for {
		select {
		case <-ctx.Done():
			partial.StopReason = message.StopReasonAborted
			partial.ErrorMessage = "request was aborted"
			return finish(partial, ctx.Err())
		case event, ok := <-events:
			if !ok {
				if err := ctx.Err(); err != nil {
					partial.StopReason = message.StopReasonAborted
					partial.ErrorMessage = "request was aborted"
					return finish(partial, err)
				}
				partial.StopReason = message.StopReasonError
				partial.ErrorMessage = "model stream closed without a terminal event"
				return finish(partial, errors.New(partial.ErrorMessage))
			}

			switch e := event.(type) {
			case provider.StreamStart:
				if !start(partial) {
					return partial, ctx.Err()
				}
			case provider.TextDelta:
				if !start(partial) {
					return partial, ctx.Err()
				}
				appendTextDelta(&partial, e.Delta)
				if !emit(ctx, ch, MessageUpdate{Message: cloneAssistantMessage(partial), Event: e}) {
					return partial, ctx.Err()
				}
			case provider.ToolCallStart:
				if !start(partial) {
					return partial, ctx.Err()
				}
				toolBlocks[e.Index] = len(partial.Content)
				partial.Content = append(partial.Content, message.ToolCall{ID: e.ID, Name: e.Name, Arguments: json.RawMessage{}})
				if !emit(ctx, ch, MessageUpdate{Message: cloneAssistantMessage(partial), Event: e}) {
					return partial, ctx.Err()
				}
			case provider.ToolCallDelta:
				if !start(partial) {
					return partial, ctx.Err()
				}
				appendToolCallDelta(&partial, toolBlocks, e.Index, e.Delta)
				if !emit(ctx, ch, MessageUpdate{Message: cloneAssistantMessage(partial), Event: e}) {
					return partial, ctx.Err()
				}
			case provider.StreamDone:
				return finish(e.Message, nil)
			case provider.StreamError:
				return finish(e.Message, e.Err)
			}
		}
	}
}

func executeToolCall(ctx context.Context, ch chan<- AgentEvent, tools []Tool, call message.ToolCall) message.ToolResultMessage {
	if !emit(ctx, ch, ToolExecStart{ToolCallID: call.ID, ToolName: call.Name, Args: call.Arguments}) {
		return errorToolResult(call, ctx.Err())
	}

	tool := findTool(tools, call.Name)
	var result ToolResult
	var err error
	if tool == nil {
		err = fmt.Errorf("tool %q was not found", call.Name)
	} else {
		result, err = tool.Execute(ctx, call.ID, call.Arguments)
	}

	toolResult := message.ToolResultMessage{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    result.Content,
		IsError:    err != nil,
		Timestamp:  time.Now().UnixMilli(),
	}
	if err != nil {
		toolResult = errorToolResult(call, err)
	}

	emit(ctx, ch, ToolExecEnd{ToolCallID: call.ID, ToolName: call.Name, Result: resultForEvent(toolResult), IsError: toolResult.IsError})
	emit(ctx, ch, MessageStart{Message: toolResult})
	emit(ctx, ch, MessageEnd{Message: toolResult})
	return toolResult
}

func resultForEvent(result message.ToolResultMessage) ToolResult {
	return ToolResult{Content: result.Content}
}

func errorToolResult(call message.ToolCall, err error) message.ToolResultMessage {
	messageText := "tool execution failed"
	if err != nil {
		messageText = fmt.Sprintf("%s: %v", messageText, err)
	}
	return message.ToolResultMessage{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    []message.ContentBlock{message.TextContent{Text: messageText}},
		IsError:    true,
		Timestamp:  time.Now().UnixMilli(),
	}
}

func findTool(tools []Tool, name string) Tool {
	for _, tool := range tools {
		if tool.ToolName() == name {
			return tool
		}
	}
	return nil
}

func toolCalls(assistant message.AssistantMessage) []message.ToolCall {
	calls := make([]message.ToolCall, 0)
	for _, block := range assistant.Content {
		if call, ok := block.(message.ToolCall); ok {
			calls = append(calls, call)
		}
	}
	return calls
}

func appendTextDelta(assistant *message.AssistantMessage, delta string) {
	if len(assistant.Content) > 0 {
		if text, ok := assistant.Content[len(assistant.Content)-1].(message.TextContent); ok {
			text.Text += delta
			assistant.Content[len(assistant.Content)-1] = text
			return
		}
	}
	assistant.Content = append(assistant.Content, message.TextContent{Text: delta})
}

func appendToolCallDelta(assistant *message.AssistantMessage, blocks map[int]int, index int, delta string) {
	blockIndex, ok := blocks[index]
	if !ok {
		blockIndex = len(assistant.Content)
		blocks[index] = blockIndex
		assistant.Content = append(assistant.Content, message.ToolCall{})
	}
	call, ok := assistant.Content[blockIndex].(message.ToolCall)
	if !ok {
		return
	}
	call.Arguments = append(call.Arguments, delta...)
	assistant.Content[blockIndex] = call
}

func cloneAssistantMessage(msg message.AssistantMessage) message.AssistantMessage {
	clone := msg
	clone.Content = make([]message.ContentBlock, len(msg.Content))
	for i, block := range msg.Content {
		switch value := block.(type) {
		case message.ToolCall:
			value.Arguments = append(json.RawMessage(nil), value.Arguments...)
			clone.Content[i] = value
		default:
			clone.Content[i] = block
		}
	}
	return clone
}

func emit(ctx context.Context, ch chan<- AgentEvent, event AgentEvent) bool {
	select {
	case ch <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func emitTerminal(ch chan<- AgentEvent, event AgentEvent) {
	ch <- event
}
