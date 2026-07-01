package config

type Config struct {
	Model ModelConfig
	Agent AgentConfig
	Tools ToolConfig
	Env   EnvConfig
}

type ModelConfig struct {
	Name      string
	Provider  string
	APIKey    string
	BaseURL   string
	MaxTokens int
}

type AgentConfig struct {
	MaxTurns     int
	SystemPrompt string
}

type ToolConfig struct {
	Read  bool
	Write bool
	Edit  bool
	Bash  bool
}

type EnvConfig struct {
	WorkingDir string
	ShellPath  string
}

func DefaultConfig() Config {
	return Config{
		Model: ModelConfig{
			Name:      "claude-sonnet-4-20250514",
			Provider:  "anthropic",
			MaxTokens: 4096,
		},
		Agent: AgentConfig{
			MaxTurns: 10,
			SystemPrompt: `You are a coding assistant. You help with software engineering tasks.
Use the available tools to read, write, and edit files.
Be concise. Think before you act.`,
		},
		Tools: ToolConfig{
			Read:  true,
			Write: false,
			Edit:  false,
			Bash:  false,
		},
		Env: EnvConfig{
			ShellPath: "/bin/bash",
		},
	}
}
