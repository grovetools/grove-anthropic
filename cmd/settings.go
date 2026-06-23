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
		Short: "Browse and tune the resolved Claude Code settings",
		Long: `Open a TUI that observes and explains what the current environment
permits Claude Code agents to do, and lets you tune the permission rules.

It resolves every settings.json scope file (Managed > CLI > Local > Project >
User) into a merged, provenance-tracked view and renders it across seven pages:
discovered scope files, the permission rule tree (allow/ask/deny with deny-wins
and managed-lock badges), the directory/sandbox boundary, a rule x scope matrix,
a live evaluation probe, an effective-summary view, and a job-centric command
browser that turns a command an agent actually ran into a synthesized allow rule
(the generalize-then-allow loop).

The Permissions, Sandbox, and Commands pages drive scope-targeted, dry-run
confirmed writes through the comment-preserving writer; the Managed scope is
never writable.

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
