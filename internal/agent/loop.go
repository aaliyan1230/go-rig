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
// Consumers must drain the channel through closure, including after cancellation:
// final messages and tool outcomes are delivered reliably for session replay.
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
	newMessages := make([]message.AgentMessage, 0, len(prompts))

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
				newMessages = append(newMessages, prompt)
				emitTerminal(ch, MessageStart{Message: prompt})
				emitTerminal(ch, MessageEnd{Message: prompt})
			}
			promptsAdded = true
		}
		turns++

		assistant, streamErr := streamAssistant(ctx, ch, client, working, cfg)
		working.Messages = append(working.Messages, assistant)
		newMessages = append(newMessages, assistant)
		if streamErr != nil {
			emitTerminal(ch, TurnEnd{Message: assistant})
			emitTerminal(ch, AgentEnd{Messages: newMessages, Err: streamErr})
			return
		}

		toolResults := make([]message.ToolResultMessage, 0)
		for _, call := range toolCalls(assistant) {
			result := executeToolCall(ctx, ch, working.Tools, call)
			toolResults = append(toolResults, result)
			working.Messages = append(working.Messages, result)
			newMessages = append(newMessages, result)
		}

		emitTerminal(ch, TurnEnd{Message: assistant, ToolResults: toolResults})
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
	start := func(msg message.AssistantMessage) {
		if started {
			return
		}
		partial = msg
		started = true
		emitTerminal(ch, MessageStart{Message: cloneAssistantMessage(partial)})
	}
	finish := func(msg message.AssistantMessage, err error) (message.AssistantMessage, error) {
		if err != nil {
			// An interrupted tool-call stream may contain invalid JSON or calls
			// that were never finalized. Keep text, but never replay those calls.
			content := make([]message.ContentBlock, 0, len(msg.Content))
			for _, block := range msg.Content {
				if _, call := block.(message.ToolCall); !call {
					content = append(content, block)
				}
			}
			msg.Content = content
			msg.StopReason = message.StopReasonError
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				msg.StopReason = message.StopReasonAborted
			}
			if msg.ErrorMessage == "" {
				msg.ErrorMessage = err.Error()
			}
		}
		start(msg)
		emitTerminal(ch, MessageEnd{Message: msg})
		return msg, err
	}
	abort := func() (message.AssistantMessage, error) {
		partial.ErrorMessage = "request was aborted"
		return finish(partial, ctx.Err())
	}
	if ctx.Err() != nil {
		return abort()
	}

	events := client.Stream(ctx, params)
	for {
		select {
		case <-ctx.Done():
			return abort()
		case event, ok := <-events:
			if !ok {
				if err := ctx.Err(); err != nil {
					return abort()
				}
				partial.StopReason = message.StopReasonError
				partial.ErrorMessage = "model stream closed without a terminal event"
				return finish(partial, errors.New(partial.ErrorMessage))
			}

			switch e := event.(type) {
			case provider.StreamStart:
				start(partial)
			case provider.TextDelta:
				start(partial)
				appendTextDelta(&partial, e.Delta)
				if !emit(ctx, ch, MessageUpdate{Message: cloneAssistantMessage(partial), Event: e}) {
					return abort()
				}
			case provider.ToolCallStart:
				start(partial)
				toolBlocks[e.Index] = len(partial.Content)
				partial.Content = append(partial.Content, message.ToolCall{ID: e.ID, Name: e.Name, Arguments: json.RawMessage{}})
				if !emit(ctx, ch, MessageUpdate{Message: cloneAssistantMessage(partial), Event: e}) {
					return abort()
				}
			case provider.ToolCallDelta:
				start(partial)
				appendToolCallDelta(&partial, toolBlocks, e.Index, e.Delta)
				if !emit(ctx, ch, MessageUpdate{Message: cloneAssistantMessage(partial), Event: e}) {
					return abort()
				}
			case provider.StreamDone:
				return finish(e.Message, nil)
			case provider.StreamError:
				if ctx.Err() != nil {
					return abort()
				}
				if len(e.Message.Content) == 0 {
					e.Message.Content = partial.Content
				}
				return finish(e.Message, e.Err)
			}
		}
	}
}

func executeToolCall(ctx context.Context, ch chan<- AgentEvent, tools []Tool, call message.ToolCall) message.ToolResultMessage {
	if ctx.Err() == nil {
		emitTerminal(ch, ToolExecStart{ToolCallID: call.ID, ToolName: call.Name, Args: call.Arguments})
	}
	tool := findTool(tools, call.Name)
	var result ToolResult
	var err error
	if ctx.Err() != nil {
		err = fmt.Errorf("not executed: canceled: %w", ctx.Err())
	} else if tool == nil {
		err = fmt.Errorf("tool %q was not found", call.Name)
	} else {
		if ctx.Err() != nil {
			err = fmt.Errorf("not executed: canceled: %w", ctx.Err())
		} else {
			result, err = tool.Execute(ctx, call.ID, call.Arguments)
		}
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

	emitTerminal(ch, ToolExecEnd{ToolCallID: call.ID, ToolName: call.Name, Result: resultForEvent(toolResult), IsError: toolResult.IsError})
	emitTerminal(ch, MessageStart{Message: toolResult})
	emitTerminal(ch, MessageEnd{Message: toolResult})
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
