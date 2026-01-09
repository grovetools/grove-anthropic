package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/mattsolo1/grove-anthropic/pkg/config"
	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/spf13/cobra"
)

var ulog = grovelogging.NewUnifiedLogger("grove-anthropic.cmd")

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
			ctx := context.Background()

			ulog.Info("Displaying API key configuration").
				Pretty("Anthropic API Key Resolution Order:\n").
				PrettyOnly().
				Log(ctx)

			// Check environment variable
			if envKey := os.Getenv("ANTHROPIC_API_KEY"); envKey != "" {
				ulog.Info("Environment variable status").
					Field("variable", "ANTHROPIC_API_KEY").
					Field("is_set", true).
					Pretty("1. Environment variable ANTHROPIC_API_KEY: set").
					PrettyOnly().
					Log(ctx)

				var maskedValue string
				if len(envKey) >= 8 {
					maskedValue = fmt.Sprintf("%s...%s", envKey[:4], envKey[len(envKey)-4:])
				} else {
					maskedValue = fmt.Sprintf("%s...", envKey[:min(4, len(envKey))])
				}

				ulog.Info("API key value masked").
					Field("masked_value", maskedValue).
					Pretty(fmt.Sprintf("   (value: %s)", maskedValue)).
					PrettyOnly().
					Log(ctx)
			} else {
				ulog.Info("Environment variable status").
					Field("variable", "ANTHROPIC_API_KEY").
					Field("is_set", false).
					Pretty("1. Environment variable ANTHROPIC_API_KEY: (not set)").
					PrettyOnly().
					Log(ctx)
			}

			// Check grove.yml
			source, found := config.GetAPIKeySource()

			if found && source == "grove.yml anthropic.api_key_command" {
				ulog.Info("Grove config status").
					Field("config_type", "api_key_command").
					Field("configured", true).
					Pretty("2. grove.yml anthropic.api_key_command: configured").
					PrettyOnly().
					Log(ctx)
			} else {
				ulog.Info("Grove config status").
					Field("config_type", "api_key_command").
					Field("configured", false).
					Pretty("2. grove.yml anthropic.api_key_command: (not configured)").
					PrettyOnly().
					Log(ctx)
			}

			if found && source == "grove.yml anthropic.api_key" {
				ulog.Info("Grove config status").
					Field("config_type", "api_key").
					Field("configured", true).
					Pretty("3. grove.yml anthropic.api_key: configured").
					PrettyOnly().
					Log(ctx)
			} else {
				ulog.Info("Grove config status").
					Field("config_type", "api_key").
					Field("configured", false).
					Pretty("3. grove.yml anthropic.api_key: (not configured)").
					PrettyOnly().
					Log(ctx)
			}

			ulog.Info("Blank line separator").
				Pretty("").
				PrettyOnly().
				Log(ctx)

			// Show current resolution
			if source, found := config.GetAPIKeySource(); found {
				ulog.Info("Current API key source resolved").
					Field("source", source).
					Pretty(fmt.Sprintf("Current API key source: %s", source)).
					PrettyOnly().
					Log(ctx)
			} else {
				ulog.Warn("No API key configured").
					Pretty("Current API key source: (none configured)\n\nTo configure, use one of:\n  export ANTHROPIC_API_KEY=your-key\n  # or in grove.yml:\n  anthropic:\n    api_key_command: \"op read 'op://vault/anthropic/api-key'\"\n  # or:\n  anthropic:\n    api_key: \"sk-ant-...\"").
					PrettyOnly().
					Log(ctx)
			}

			return nil
		},
	}
}
