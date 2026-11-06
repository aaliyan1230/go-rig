// Package app owns a conversation across agent runs. Memory is the live state;
// the session is an independent, best-effort durable copy of completed messages.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/policy"
	"github.com/aaliyan1230/go-rig/internal/provider"
	"github.com/aaliyan1230/go-rig/internal/session"
)

type Session interface {
	Messages() []message.AgentMessage
	Append(context.Context, message.AgentMessage) error
	Close() error
}

type Options struct {
	Client            provider.ModelClient
	Config            agent.RunConfig
	Tools             []agent.Tool
	SystemPrompt      string
	Cwd               string
	Session           Session
	CreateSession     func(context.Context) (Session, error)
	BuildSystemPrompt func(context.Context) (string, error)
	Warn              func(error)
	Broker            *policy.Broker
	Confirm           policy.Confirm
	Controls          string
	Cleanup           func() error
}

type Info struct {
	Metadata           session.Metadata
	Cwd                string
	Model              string
	MessageCount       int
	PersistenceEnabled bool
	CanContinue        bool
	Controls           string
	BatchSize          int
}

type Result struct {
	Status agent.EndReason
	Turns  int
}

// Conversation methods are called serially. Cancel a running Prompt through its
// context; its event callback must return normally so final events can drain.
type Conversation struct {
	opts               Options
	messages           []message.AgentMessage
	persistenceEnabled bool
	closed             bool
}

func New(opts Options) *Conversation {
	opts.Tools = append([]agent.Tool(nil), opts.Tools...)
	c := &Conversation{opts: opts, persistenceEnabled: opts.Session != nil}
	if opts.Session != nil {
		c.messages = append([]message.AgentMessage(nil), opts.Session.Messages()...)
	}
	return c
}

func (c *Conversation) Info() Info {
	info := Info{Cwd: c.opts.Cwd, Model: c.opts.Config.Model, MessageCount: len(c.messages), PersistenceEnabled: c.persistenceEnabled, CanContinue: agent.CanContinue(c.messages), Controls: c.opts.Controls, BatchSize: c.opts.Config.MaxTurns}
	if s, ok := c.opts.Session.(interface{ Metadata() session.Metadata }); ok {
		info.Metadata = s.Metadata()
	}
	return info
}

func (c *Conversation) SetModel(name string) error {
	if c.closed {
		return errors.New("conversation is closed")
	}
	if len(strings.Fields(name)) != 1 {
		return errors.New("usage: /model <name>")
	}
	c.opts.Config.Model = strings.TrimSpace(name)
	return nil
}

func (c *Conversation) Prompt(ctx context.Context, text string, onEvent func(agent.AgentEvent)) (Result, error) {
	if strings.TrimSpace(text) == "" {
		return Result{}, errors.New("prompt cannot be blank")
	}
	prompt := message.UserMessage{Content: []message.ContentBlock{message.TextContent{Text: text}}, Timestamp: time.Now().UnixMilli()}
	return c.run(ctx, []message.AgentMessage{prompt}, onEvent, false)
}

func (c *Conversation) Continue(ctx context.Context, onEvent func(agent.AgentEvent)) (Result, error) {
	return c.run(ctx, nil, onEvent, true)
}

func (c *Conversation) run(ctx context.Context, prompts []message.AgentMessage, onEvent func(agent.AgentEvent), continuing bool) (Result, error) {
	if c.closed {
		return Result{}, errors.New("conversation is closed")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cfg := c.opts.Config
	var requests <-chan policy.Pending
	if c.opts.Broker != nil {
		requests = c.opts.Broker.Requests()
		if cfg.OnTurnLimit == nil && c.opts.Confirm != nil {
			cfg.OnTurnLimit = func(ctx context.Context, info agent.TurnLimitInfo) (bool, error) {
				return c.opts.Broker.Ask(ctx, policy.Request{Kind: "turns", Title: fmt.Sprintf("Continue for another %d turns?", info.BatchSize), Details: fmt.Sprintf("Completed %d model requests in this run. Completed tool outcomes have been recorded.", info.Turns)})
			}
		}
	}
	initial := agent.AgentContext{
		SystemPrompt: c.opts.SystemPrompt, Messages: c.messages, Tools: c.opts.Tools,
	}
	var events <-chan agent.AgentEvent
	if continuing {
		events = agent.Continue(ctx, c.opts.Client, initial, cfg)
	} else {
		events = agent.Run(ctx, c.opts.Client, prompts, initial, cfg)
	}
	var runErr error
	var result Result
	handle := func(event agent.AgentEvent) {
		if e, ok := event.(agent.MessageEnd); ok {
			c.messages = append(c.messages, e.Message)
			if c.persistenceEnabled {
				// Cancellation stops model/tool work, not recording its final outcome.
				if err := c.opts.Session.Append(context.WithoutCancel(ctx), e.Message); err != nil {
					c.persistenceEnabled = false
					c.warn(fmt.Errorf("session persistence disabled: %w", err))
				}
			}
		}
		if e, ok := event.(agent.AgentEnd); ok {
			runErr = e.Err
			result = Result{Status: e.Reason, Turns: e.Turns}
		}
		if onEvent != nil {
			onEvent(event)
		}
	}
	for events != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			handle(event)
		case pending := <-requests:
			// The producer is waiting for a reply: drain its already-emitted
			// messages before showing any UI or accepting a decision.
		drain:
			for events != nil {
				select {
				case event, ok := <-events:
					if !ok {
						events = nil
					} else {
						handle(event)
					}
				default:
					break drain
				}
			}
			var answer policy.Answer
			if pending.Context.Err() != nil {
				answer.Err = pending.Context.Err()
			} else if c.opts.Confirm != nil {
				answer.Allowed, answer.Err = c.opts.Confirm(pending.Context, pending.Request)
			}
			if answer.Err != nil {
				cancel()
			}
			pending.Reply <- answer
		}
	}
	return result, runErr
}

func (c *Conversation) NewSession(ctx context.Context) error {
	if c.closed {
		return errors.New("conversation is closed")
	}
	if c.opts.CreateSession == nil {
		return errors.New("creating sessions is not configured")
	}
	system := c.opts.SystemPrompt
	if c.opts.BuildSystemPrompt != nil {
		var err error
		system, err = c.opts.BuildSystemPrompt(ctx)
		if err != nil {
			return err
		}
	}
	next, err := c.opts.CreateSession(ctx)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	previous := c.opts.Session
	c.opts.Session = next
	c.opts.SystemPrompt = system
	c.messages = nil
	c.persistenceEnabled = true
	if previous != nil {
		if err := previous.Close(); err != nil {
			c.warn(fmt.Errorf("close session: %w", err))
		}
	}
	return nil
}

func (c *Conversation) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	c.persistenceEnabled = false
	var err error
	if c.opts.Session != nil {
		err = c.opts.Session.Close()
	}
	if c.opts.Cleanup != nil {
		err = errors.Join(err, c.opts.Cleanup())
	}
	return err
}

func (c *Conversation) warn(err error) {
	if c.opts.Warn != nil {
		c.opts.Warn(err)
	}
}
