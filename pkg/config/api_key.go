package config

import (
	"fmt"
	"os"
)

// ResolveAPIKey resolves the Anthropic API key from the ANTHROPIC_API_KEY environment variable.
func ResolveAPIKey() (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("Anthropic API key not found. Please set the ANTHROPIC_API_KEY environment variable")
	}
	return apiKey, nil
}
