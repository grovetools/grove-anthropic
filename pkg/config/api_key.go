package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	core_config "github.com/mattsolo1/grove-core/config"
	core_errors "github.com/mattsolo1/grove-core/errors"
)

// AnthropicConfig defines the structure for the 'anthropic' extension in grove.yml
type AnthropicConfig struct {
	APIKey        string `yaml:"api_key"`
	APIKeyCommand string `yaml:"api_key_command"`
}

// ResolveAPIKey resolves the Anthropic API key from multiple sources in order of precedence:
// 1. ANTHROPIC_API_KEY environment variable
// 2. Command output from anthropic.api_key_command in grove.yml
// 3. Direct value from anthropic.api_key in grove.yml
func ResolveAPIKey() (string, error) {
	// First priority: Environment variable
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		return apiKey, nil
	}

	// Second and third priority: grove.yml configuration
	cfg, err := core_config.LoadDefault()
	if err != nil {
		// Check if it's a "config not found" error
		if core_errors.Is(err, core_errors.ErrCodeConfigNotFound) {
			// No config file - this is okay, but we have no API key
			return "", fmt.Errorf("Anthropic API key not found. Please configure it using one of:\n" +
				"  1. Set ANTHROPIC_API_KEY environment variable\n" +
				"  2. Add 'anthropic.api_key_command' to grove.yml\n" +
				"  3. Add 'anthropic.api_key' to grove.yml")
		}
		// Some other error loading config
		return "", fmt.Errorf("failed to load grove.yml: %w", err)
	}

	// Parse the anthropic extension
	var anthropicCfg AnthropicConfig
	if err := cfg.UnmarshalExtension("anthropic", &anthropicCfg); err != nil {
		// Extension doesn't exist or couldn't be parsed - that's okay, check for empty
	}

	// Second priority: Command execution
	if anthropicCfg.APIKeyCommand != "" {
		cmd := exec.Command("sh", "-c", anthropicCfg.APIKeyCommand)
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to execute api_key_command '%s': %w", anthropicCfg.APIKeyCommand, err)
		}
		apiKey := strings.TrimSpace(string(output))
		if apiKey == "" {
			return "", fmt.Errorf("api_key_command '%s' returned empty output", anthropicCfg.APIKeyCommand)
		}
		return apiKey, nil
	}

	// Third priority: Direct API key
	if anthropicCfg.APIKey != "" {
		return anthropicCfg.APIKey, nil
	}

	// No API key found anywhere
	return "", fmt.Errorf("Anthropic API key not found. Please configure it using one of:\n" +
		"  1. Set ANTHROPIC_API_KEY environment variable\n" +
		"  2. Add 'anthropic.api_key_command' to grove.yml\n" +
		"  3. Add 'anthropic.api_key' to grove.yml")
}

// GetAPIKeySource returns information about where the API key would be resolved from
// without returning the actual key value. Returns (source, found).
func GetAPIKeySource() (string, bool) {
	// Check environment variable
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "environment variable ANTHROPIC_API_KEY", true
	}

	// Check grove.yml
	cfg, err := core_config.LoadDefault()
	if err != nil {
		return "", false
	}

	var anthropicCfg AnthropicConfig
	if err := cfg.UnmarshalExtension("anthropic", &anthropicCfg); err != nil {
		return "", false
	}

	if anthropicCfg.APIKeyCommand != "" {
		return "grove.yml anthropic.api_key_command", true
	}

	if anthropicCfg.APIKey != "" {
		return "grove.yml anthropic.api_key", true
	}

	return "", false
}
