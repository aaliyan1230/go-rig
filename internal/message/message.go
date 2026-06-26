package message

// StopReason indicates why the assistant stopped generating.
type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)

type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type Usage struct {
	Input      int  `json:"input"`
	Output     int  `json:"output"`
	CacheRead  int  `json:"cacheRead"`
	CacheWrite int  `json:"cacheWrite"`
	Cost       Cost `json:"cost"`
}

// Message is a sealed interface for provider-facing messages.
// Only types in this package can implement it.
type Message interface {
	role() string
	getTimestamp() int64
	message()
}

type UserMessage struct {
	Content   []ContentBlock `json:"content"`
	Timestamp int64          `json:"timestamp"`
}

func (UserMessage) role() string        { return "user" }
func (m UserMessage) getTimestamp() int64 { return m.Timestamp }
func (UserMessage) message()            {}

type AssistantMessage struct {
	Content      []ContentBlock `json:"content"`
	Model        string         `json:"model"`
	Provider     string         `json:"provider"`
	Usage        Usage          `json:"usage"`
	StopReason   StopReason     `json:"stopReason"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
	Timestamp    int64          `json:"timestamp"`
}

func (AssistantMessage) role() string        { return "assistant" }
func (m AssistantMessage) getTimestamp() int64 { return m.Timestamp }
func (AssistantMessage) message()            {}

type ToolResultMessage struct {
	ToolCallID string         `json:"toolCallId"`
	ToolName   string         `json:"toolName"`
	Content    []ContentBlock `json:"content"`
	IsError    bool           `json:"isError"`
	Timestamp  int64          `json:"timestamp"`
}

func (ToolResultMessage) role() string        { return "toolResult" }
func (m ToolResultMessage) getTimestamp() int64 { return m.Timestamp }
func (ToolResultMessage) message()            {}
