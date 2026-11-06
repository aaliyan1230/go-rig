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
	MaxTurns int
	// SystemPrompt, when non-empty, replaces the built-in preamble, tool
	// list, and rules. Project context and cwd are still appended. Empty
	// means use the default prompt from internal/prompt.
	SystemPrompt string
}

type ToolConfig struct {
	Read  bool
	Write bool
	Edit  bool
	Bash  bool
}

type EnvConfig struct {
	WorkingDir  string
	ShellPath   string
	BashTimeout int
}

func DefaultConfig() Config {
	return Config{
		Model: ModelConfig{
			Name:      "gpt-5.4-mini",
			Provider:  "openai",
			MaxTokens: 4096,
		},
		Agent: AgentConfig{
			MaxTurns: 50,
		},
		Tools: ToolConfig{
			Read:  true,
			Write: true,
			Edit:  true,
			Bash:  true,
		},
		Env: EnvConfig{
			ShellPath:   "/bin/bash",
			BashTimeout: 30,
		},
	}
}
