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
	BuildSystemPrompt func(context.Context) string
	Warn              func(error)
}

type Info struct {
	Metadata           session.Metadata
	Cwd                string
	Model              string
	MessageCount       int
	PersistenceEnabled bool
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
	info := Info{Cwd: c.opts.Cwd, Model: c.opts.Config.Model, MessageCount: len(c.messages), PersistenceEnabled: c.persistenceEnabled}
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

func (c *Conversation) Prompt(ctx context.Context, text string, onEvent func(agent.AgentEvent)) error {
	if c.closed {
		return errors.New("conversation is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("prompt cannot be blank")
	}
	prompt := message.UserMessage{Content: []message.ContentBlock{message.TextContent{Text: text}}, Timestamp: time.Now().UnixMilli()}
	events := agent.Run(ctx, c.opts.Client, []message.AgentMessage{prompt}, agent.AgentContext{
		SystemPrompt: c.opts.SystemPrompt, Messages: c.messages, Tools: c.opts.Tools,
	}, c.opts.Config)
	var runErr error
	for event := range events {
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
		}
		if onEvent != nil {
			onEvent(event)
		}
	}
	return runErr
}

func (c *Conversation) NewSession(ctx context.Context) error {
	if c.closed {
		return errors.New("conversation is closed")
	}
	if c.opts.CreateSession == nil {
		return errors.New("creating sessions is not configured")
	}
	next, err := c.opts.CreateSession(ctx)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	system := c.opts.SystemPrompt
	if c.opts.BuildSystemPrompt != nil {
		system = c.opts.BuildSystemPrompt(ctx)
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
	if c.opts.Session != nil {
		return c.opts.Session.Close()
	}
	return nil
}

func (c *Conversation) warn(err error) {
	if c.opts.Warn != nil {
		c.opts.Warn(err)
	}
}
