package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// BasicScenario tests basic CLI functionality that doesn't require API calls
func BasicScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "grove-anthropic-basic",
		Description: "Basic CLI functionality tests (no API calls)",
		Tags:        []string{"smoke", "cli"},
		Steps: []harness.Step{
			harness.NewStep("version command", func(ctx *harness.Context) error {
				binary, err := FindBinary()
				if err != nil {
					return err
				}
				cmd := ctx.Command(binary, "version")
				cmd.Dir(ctx.RootDir)
				result := cmd.Run()

				if err := result.AssertSuccess(); err != nil {
					return fmt.Errorf("version command failed: %w\nStderr: %s", err, result.Stderr)
				}
				if !strings.Contains(result.Stdout, "Version:") {
					return fmt.Errorf("expected version output to contain 'Version:', got: %s", result.Stdout)
				}
				return nil
			}),
			harness.NewStep("help command", func(ctx *harness.Context) error {
				binary, err := FindBinary()
				if err != nil {
					return err
				}
				cmd := ctx.Command(binary, "--help")
				cmd.Dir(ctx.RootDir)
				result := cmd.Run()

				if err := result.AssertSuccess(); err != nil {
					return fmt.Errorf("help command failed: %w", err)
				}
				if !strings.Contains(result.Stdout, "Tools for using Anthropic/Claude API") {
					return fmt.Errorf("expected help output to contain description")
				}
				return nil
			}),
			harness.NewStep("request command shows help", func(ctx *harness.Context) error {
				binary, err := FindBinary()
				if err != nil {
					return err
				}
				cmd := ctx.Command(binary, "request", "--help")
				cmd.Dir(ctx.RootDir)
				result := cmd.Run()

				if err := result.AssertSuccess(); err != nil {
					return fmt.Errorf("request --help failed: %w", err)
				}
				if !strings.Contains(result.Stdout, "--model") {
					return fmt.Errorf("expected request help to show --model flag")
				}
				if !strings.Contains(result.Stdout, "--prompt") {
					return fmt.Errorf("expected request help to show --prompt flag")
				}
				return nil
			}),
		},
	}
}

// RequestScenario tests the actual API request functionality
// This requires ANTHROPIC_API_KEY to be set
func RequestScenario() *harness.Scenario {
	return harness.NewScenarioWithOptions(
		"grove-anthropic-request",
		"Tests API request functionality (requires ANTHROPIC_API_KEY)",
		[]string{"api", "anthropic"},
		[]harness.Step{
			harness.NewStep("check API key", func(ctx *harness.Context) error {
				if os.Getenv("ANTHROPIC_API_KEY") == "" {
					return fmt.Errorf("ANTHROPIC_API_KEY not set - skipping API tests")
				}
				return nil
			}),
			harness.NewStep("setup test context file", func(ctx *harness.Context) error {
				contextContent := "The capital of France is Paris."
				contextFile := filepath.Join(ctx.RootDir, "test_context.txt")
				if err := fs.WriteString(contextFile, contextContent); err != nil {
					return fmt.Errorf("failed to write context file: %w", err)
				}
				ctx.Set("context_file", contextFile)
				return nil
			}),
			harness.NewStep("execute request command", func(ctx *harness.Context) error {
				binary, err := FindBinary()
				if err != nil {
					return err
				}

				contextFile := ctx.GetString("context_file")

				// ctx.Command() automatically sets sandboxed HOME, XDG_CONFIG_HOME, etc.
				cmd := ctx.Command(binary,
					"request",
					"-p", "What is the capital mentioned in the context file? Reply with just the city name.",
					"--context", contextFile,
					"--max-tokens", "50",
				)
				cmd.Dir(ctx.RootDir)

				result := cmd.Run()
				ctx.Set("result", result)

				if err := result.AssertSuccess(); err != nil {
					return fmt.Errorf("request command failed: %w\nStderr: %s", err, result.Stderr)
				}
				return nil
			}),
			harness.NewStep("verify response", func(ctx *harness.Context) error {
				result := ctx.Get("result")
				if result == nil {
					return fmt.Errorf("no result found from previous step")
				}

				// Type assert to get stdout
				type hasStdout interface {
					GetStdout() string
				}
				if r, ok := result.(hasStdout); ok {
					stdout := r.GetStdout()
					if strings.TrimSpace(stdout) == "" {
						return fmt.Errorf("expected non-empty response, got empty output")
					}
					ctx.ShowCommandOutput("API Response", stdout, "")
				}
				return nil
			}),
		},
		false, // localOnly
		true,  // explicitOnly - only run when explicitly requested (needs API key)
	)
}
