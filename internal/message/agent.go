package message

type AgentMessage interface {
	agentRole() string
	agentTimestamp() int64
	agentMessage()
}

// Ensure all Message types also satisfy AgentMessage.
func (m UserMessage) agentRole() string        { return m.role() }
func (m UserMessage) agentTimestamp() int64     { return m.Timestamp }
func (UserMessage) agentMessage()              {}

func (m AssistantMessage) agentRole() string    { return m.role() }
func (m AssistantMessage) agentTimestamp() int64 { return m.Timestamp }
func (AssistantMessage) agentMessage()          {}

func (m ToolResultMessage) agentRole() string    { return m.role() }
func (m ToolResultMessage) agentTimestamp() int64 { return m.Timestamp }
func (ToolResultMessage) agentMessage()          {}

func ConvertToLLM(msgs []AgentMessage) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if msg, ok := m.(Message); ok {
			out = append(out, msg)
		}
	}
	return out
}
