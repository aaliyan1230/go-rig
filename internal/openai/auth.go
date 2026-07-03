package openai

import (
	"fmt"
	"os"
)

// ResolveAPIKey looks up the API key
func ResolveAPIKey(configKey string) (string, error) {
	if configKey != "" {
		return configKey, nil
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("no API key: set OPENAI_API_KEY or use --api-key flag")
}
