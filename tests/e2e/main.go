package main

import (
	"context"
	"fmt"
	"os"

	"github.com/grovetools/tend/pkg/app"
	"github.com/grovetools/tend/pkg/harness"
)

func main() {
	// A list of all E2E scenarios
	scenarios := []*harness.Scenario{
		// Basic Scenarios (no API key required)
		BasicScenario(),
		// Config Scenarios
		APIKeyConfigScenario(),
		// API Scenarios (requires ANTHROPIC_API_KEY, explicit-only)
		RequestScenario(),
	}

	// Execute the custom tend application with our scenarios
	if err := app.Execute(context.Background(), scenarios); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
