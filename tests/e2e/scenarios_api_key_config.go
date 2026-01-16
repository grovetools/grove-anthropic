package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// APIKeyConfigScenario tests API key configuration from various sources
// Note: ctx.Command() automatically injects sandboxed HOME, XDG_CONFIG_HOME,
// XDG_DATA_HOME, and XDG_CACHE_HOME - no need to set them manually.
func APIKeyConfigScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "grove-anthropic-api-key-config",
		Description: "Test API key configuration from various sources",
		Tags:        []string{"config", "api-key"},
		Steps: []harness.Step{
			harness.NewStep("error when no API key configured", func(ctx *harness.Context) error {
				binary, err := FindBinary()
				if err != nil {
					return err
				}

				// Run request without any API key configured
				// Override any inherited ANTHROPIC_API_KEY with empty string
				cmd := ctx.Command(binary, "request", "-p", "test")
				cmd.Dir(ctx.RootDir)
				cmd.Env("ANTHROPIC_API_KEY=")

				result := cmd.Run()

				// Should fail with proper error message
				if result.ExitCode == 0 {
					return fmt.Errorf("expected command to fail without API key, but it succeeded")
				}
				if !strings.Contains(result.Stderr, "Anthropic API key not found") {
					return fmt.Errorf("expected error message about missing API key, got: %s", result.Stderr)
				}
				if !strings.Contains(result.Stderr, "ANTHROPIC_API_KEY environment variable") {
					return fmt.Errorf("expected error to mention ANTHROPIC_API_KEY, got: %s", result.Stderr)
				}
				if !strings.Contains(result.Stderr, "anthropic.api_key_command") {
					return fmt.Errorf("expected error to mention anthropic.api_key_command, got: %s", result.Stderr)
				}
				return nil
			}),

			harness.NewStep("API key from environment variable", func(ctx *harness.Context) error {
				binary, err := FindBinary()
				if err != nil {
					return err
				}

				// Run request with API key in environment (invalid key will fail at API level)
				cmd := ctx.Command(binary, "request", "-p", "test")
				cmd.Dir(ctx.RootDir)
				cmd.Env("ANTHROPIC_API_KEY=test-key-from-env")

				result := cmd.Run()

				// Should fail with API key validation error (not missing key error)
				if result.ExitCode == 0 {
					return fmt.Errorf("expected command to fail with invalid API key")
				}
				if strings.Contains(result.Stderr, "Anthropic API key not found") {
					return fmt.Errorf("should not show 'key not found' error when key is provided via env")
				}
				// The API should reject the invalid key
				return nil
			}),

			harness.NewStep("API key from grove.yml api_key_command", func(ctx *harness.Context) error {
				binary, err := FindBinary()
				if err != nil {
					return err
				}

				// Create grove.yml with api_key_command in working directory
				groveYml := `name: test-project
anthropic:
  api_key_command: "echo test-key-from-command"
`
				if err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveYml); err != nil {
					return fmt.Errorf("failed to write grove.yml: %w", err)
				}

				cmd := ctx.Command(binary, "request", "-p", "test")
				cmd.Dir(ctx.RootDir)
				cmd.Env("ANTHROPIC_API_KEY=") // Override inherited env var

				result := cmd.Run()

				// Should fail with API validation error (not missing key error)
				if result.ExitCode == 0 {
					return fmt.Errorf("expected command to fail with invalid API key")
				}
				if strings.Contains(result.Stderr, "Anthropic API key not found") {
					return fmt.Errorf("should not show 'key not found' error when key is from command")
				}
				return nil
			}),

			harness.NewStep("API key from grove.yml direct value", func(ctx *harness.Context) error {
				binary, err := FindBinary()
				if err != nil {
					return err
				}

				// Create grove.yml with direct api_key
				groveYml := `name: test-project
anthropic:
  api_key: "test-key-direct"
`
				if err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveYml); err != nil {
					return fmt.Errorf("failed to write grove.yml: %w", err)
				}

				cmd := ctx.Command(binary, "request", "-p", "test")
				cmd.Dir(ctx.RootDir)
				cmd.Env("ANTHROPIC_API_KEY=") // Override inherited env var

				result := cmd.Run()

				// Should fail with API validation error (not missing key error)
				if strings.Contains(result.Stderr, "Anthropic API key not found") {
					return fmt.Errorf("should not show 'key not found' error when key is in config")
				}
				return nil
			}),

			harness.NewStep("env var takes precedence over grove.yml", func(ctx *harness.Context) error {
				binary, err := FindBinary()
				if err != nil {
					return err
				}

				// Create grove.yml with different keys
				groveYml := `name: test-project
anthropic:
  api_key: "key-from-config-should-be-ignored"
  api_key_command: "echo key-from-command-should-be-ignored"
`
				if err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveYml); err != nil {
					return fmt.Errorf("failed to write grove.yml: %w", err)
				}

				cmd := ctx.Command(binary, "request", "-p", "test")
				cmd.Dir(ctx.RootDir)
				cmd.Env("ANTHROPIC_API_KEY=key-from-env-precedence")

				result := cmd.Run()

				// The env var key should be used (will fail with API error, not config error)
				if strings.Contains(result.Stderr, "failed to load grove.yml") {
					return fmt.Errorf("should not have config loading errors: %s", result.Stderr)
				}
				return nil
			}),

			harness.NewStep("config get api-key shows resolution", func(ctx *harness.Context) error {
				binary, err := FindBinary()
				if err != nil {
					return err
				}

				cmd := ctx.Command(binary, "config", "get", "api-key")
				cmd.Dir(ctx.RootDir)
				cmd.Env("ANTHROPIC_API_KEY=test-key-123456789")

				result := cmd.Run()

				if err := result.AssertSuccess(); err != nil {
					return fmt.Errorf("config get api-key failed: %w", err)
				}

				if !strings.Contains(result.Stdout, "ANTHROPIC_API_KEY") {
					return fmt.Errorf("expected output to mention ANTHROPIC_API_KEY")
				}
				if !strings.Contains(result.Stdout, "set") {
					return fmt.Errorf("expected output to show env var is set")
				}
				return nil
			}),
		},
	}
}
