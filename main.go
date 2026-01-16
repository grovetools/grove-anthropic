package main

import (
	"os"

	"github.com/grovetools/grove-anthropic/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
