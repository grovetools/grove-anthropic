// Package models provides centralized model definitions for Anthropic Claude models.
package models

// Model represents an LLM model with its metadata.
type Model struct {
	ID       string  // Full API model ID (e.g., "claude-sonnet-4-5-20250929")
	Alias    string  // Short alias (e.g., "claude-sonnet-4-5"), empty if none
	Provider string  // Provider name (e.g., "Anthropic")
	Note     string  // Human-readable description
	Input    float64 // Input price per million tokens
	Output   float64 // Output price per million tokens
	Legacy   bool    // Whether this is a legacy model
}

// DefaultModel is the recommended default model to use.
const DefaultModel = "claude-sonnet-4-5-20250929"

// DefaultAlias is the alias for the default model.
const DefaultAlias = "claude-sonnet-4-5"

// LongContextThreshold is the token count above which long context pricing applies.
const LongContextThreshold int64 = 200_000

// Models returns all available Anthropic Claude models.
func Models() []Model {
	return []Model{
		// Claude 4.5 models (current)
		{
			ID:       "claude-sonnet-4-5-20250929",
			Alias:    "claude-sonnet-4-5",
			Provider: "Anthropic",
			Note:     "Sonnet 4.5 - best balance of speed and intelligence",
			Input:    3.00,
			Output:   15.00,
			Legacy:   false,
		},
		{
			ID:       "claude-haiku-4-5-20251001",
			Alias:    "claude-haiku-4-5",
			Provider: "Anthropic",
			Note:     "Haiku 4.5 - fastest with near-frontier intelligence",
			Input:    1.00,
			Output:   5.00,
			Legacy:   false,
		},
		{
			ID:       "claude-opus-4-5-20251101",
			Alias:    "claude-opus-4-5",
			Provider: "Anthropic",
			Note:     "Opus 4.5 - maximum intelligence",
			Input:    5.00,
			Output:   25.00,
			Legacy:   false,
		},
		// Claude 4.x legacy
		{
			ID:       "claude-sonnet-4-20250514",
			Alias:    "claude-sonnet-4",
			Provider: "Anthropic",
			Note:     "Sonnet 4 (legacy)",
			Input:    3.00,
			Output:   15.00,
			Legacy:   true,
		},
		{
			ID:       "claude-opus-4-20250514",
			Alias:    "claude-opus-4",
			Provider: "Anthropic",
			Note:     "Opus 4 (legacy)",
			Input:    15.00,
			Output:   75.00,
			Legacy:   true,
		},
		{
			ID:       "claude-3-7-sonnet-20250219",
			Alias:    "claude-3-7-sonnet",
			Provider: "Anthropic",
			Note:     "Sonnet 3.7 (legacy)",
			Input:    3.00,
			Output:   15.00,
			Legacy:   true,
		},
		// Claude 3.x legacy
		{
			ID:       "claude-3-haiku-20240307",
			Alias:    "claude-3-haiku",
			Provider: "Anthropic",
			Note:     "Haiku 3 (legacy)",
			Input:    0.25,
			Output:   1.25,
			Legacy:   true,
		},
	}
}

// Aliases returns a map of alias -> full model ID for all models with aliases.
func Aliases() map[string]string {
	aliases := make(map[string]string)
	for _, m := range Models() {
		if m.Alias != "" {
			aliases[m.Alias] = m.ID
		}
	}
	return aliases
}

// ResolveAlias expands a model alias to its full API ID, or returns the input unchanged.
func ResolveAlias(model string) string {
	if fullID, ok := Aliases()[model]; ok {
		return fullID
	}
	return model
}

// CurrentModels returns only non-legacy models (for TUI pickers, etc.).
func CurrentModels() []Model {
	var current []Model
	for _, m := range Models() {
		if !m.Legacy {
			current = append(current, m)
		}
	}
	return current
}

// GetPricing returns input and output price per million tokens for a model.
// Returns default Sonnet 4.5 pricing if model not found.
func GetPricing(model string) (input, output float64) {
	// Resolve alias first
	model = ResolveAlias(model)

	for _, m := range Models() {
		if m.ID == model {
			return m.Input, m.Output
		}
	}
	// Default to Sonnet 4.5 pricing
	return 3.00, 15.00
}
