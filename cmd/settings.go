package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/embed"
	"github.com/spf13/cobra"

	"github.com/grovetools/grove-anthropic/pkg/tui/settings"
)

func newSettingsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Browse the resolved Claude Code settings (read-only)",
		Long: `Open a read-only TUI that observes and explains what the current
environment permits Claude Code agents to do.

It resolves every settings.json scope file (Managed > CLI > Local > Project >
User) into a merged, provenance-tracked view and renders it across six pages:
discovered scope files, the permission rule tree (allow/ask/deny with deny-wins
and managed-lock badges), the directory/sandbox boundary, a rule x scope matrix,
a live evaluation probe, and an effective-summary view.

With --json, the merged settings and provenance are printed to stdout instead
of opening the TUI (no terminal required), mirroring 'grove config --json'.`,
		RunE: runSettings,
	}
	return cmd
}

func runSettings(cmd *cobra.Command, args []string) error {
	data, err := settings.Load()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	// Headless path: --json prints the merged config to stdout without a TTY.
	if jsonOutput, _ := cmd.Flags().GetBool("json"); jsonOutput {
		return settings.PrintJSON(data, os.Stdout)
	}

	m := settings.New(data)
	_, err = embed.RunStandalone(m, tea.WithAltScreen())
	return err
}
