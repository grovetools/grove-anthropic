package cmd

import (
	"github.com/grovetools/core/cli"
	"github.com/spf13/cobra"
)

var rootCmd *cobra.Command

func init() {
	rootCmd = cli.NewStandardCommand("grove-anthropic", "Tools for using Anthropic/Claude API plaform")

	// Add commands
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newRequestCmd())
	rootCmd.AddCommand(newConfigCmd())
}

func Execute() error {
	return rootCmd.Execute()
}
