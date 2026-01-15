package cmd

import (
	"fmt"
	"os"

	"github.com/mattsolo1/grove-anthropic/pkg/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage grove-anthropic configuration",
		Long:  `View and manage configuration settings for grove-anthropic.`,
	}

	cmd.AddCommand(newConfigGetCmd())

	return cmd
}

func newConfigGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get configuration values",
	}

	cmd.AddCommand(newConfigGetAPIKeyCmd())

	return cmd
}

func newConfigGetAPIKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "api-key",
		Short: "Show API key configuration and resolution",
		Long: `Show where the Anthropic API key is configured and which source will be used.

Resolution order:
  1. ANTHROPIC_API_KEY environment variable
  2. anthropic.api_key_command in grove.yml (executes shell command)
  3. anthropic.api_key in grove.yml (direct value)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Anthropic API Key Resolution Order:")

			// Check environment variable
			if envKey := os.Getenv("ANTHROPIC_API_KEY"); envKey != "" {
				fmt.Println("1. Environment variable ANTHROPIC_API_KEY: set")

				var maskedValue string
				if len(envKey) >= 8 {
					maskedValue = fmt.Sprintf("%s...%s", envKey[:4], envKey[len(envKey)-4:])
				} else {
					maskedValue = fmt.Sprintf("%s...", envKey[:min(4, len(envKey))])
				}
				fmt.Printf("   (value: %s)\n", maskedValue)
			} else {
				fmt.Println("1. Environment variable ANTHROPIC_API_KEY: (not set)")
			}

			// Check grove.yml
			source, found := config.GetAPIKeySource()

			if found && source == "grove.yml anthropic.api_key_command" {
				fmt.Println("2. grove.yml anthropic.api_key_command: configured")
			} else {
				fmt.Println("2. grove.yml anthropic.api_key_command: (not configured)")
			}

			if found && source == "grove.yml anthropic.api_key" {
				fmt.Println("3. grove.yml anthropic.api_key: configured")
			} else {
				fmt.Println("3. grove.yml anthropic.api_key: (not configured)")
			}

			fmt.Println()

			// Show current resolution
			if source, found := config.GetAPIKeySource(); found {
				fmt.Printf("Current API key source: %s\n", source)
			} else {
				fmt.Println("Current API key source: (none configured)")
				fmt.Println()
				fmt.Println("To configure, use one of:")
				fmt.Println("  export ANTHROPIC_API_KEY=your-key")
				fmt.Println("  # or in grove.yml:")
				fmt.Println("  anthropic:")
				fmt.Println("    api_key_command: \"op read 'op://vault/anthropic/api-key'\"")
				fmt.Println("  # or:")
				fmt.Println("  anthropic:")
				fmt.Println("    api_key: \"sk-ant-...\"")
			}

			return nil
		},
	}
}
