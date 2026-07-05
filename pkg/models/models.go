// Package models provides centralized model definitions for Anthropic Claude models.
package models

import "strings"

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
const DefaultModel = "claude-sonnet-4-6-20260115"

// DefaultAlias is the alias for the default model.
const DefaultAlias = "claude-sonnet-4-6"

// DefaultAgentAlias is the default model alias for claude agent jobs
// (interactive/headless/isolated). Distinct from DefaultAlias, which is
// the oneshot/chat planning default.
const DefaultAgentAlias = "claude-fable-5"

// LongContextThreshold is the token count above which long context pricing applies.
const LongContextThreshold int64 = 200_000

// Models returns all available Anthropic Claude models.
func Models() []Model {
	return []Model{
		// Claude 5 models (current)
		{
			ID:       "claude-fable-5",
			Alias:    "claude-fable-5",
			Provider: "Anthropic",
			Note:     "Fable 5 - most capable Anthropic model for demanding reasoning and long-horizon agentic work",
			Input:    10.00,
			Output:   50.00,
			Legacy:   false,
		},
		// Claude 4.8 models
		{
			ID:       "claude-opus-4-8",
			Alias:    "claude-opus-4-8",
			Provider: "Anthropic",
			Note:     "Opus 4.8 - top Anthropic coding and agentic model",
			Input:    5.00,
			Output:   25.00,
			Legacy:   false,
		},
		// Claude 4.6 models
		{
			ID:       "claude-opus-4-6-20260115",
			Alias:    "claude-opus-4-6",
			Provider: "Anthropic",
			Note:     "Opus 4.6 - most intelligent for agents and coding",
			Input:    5.00,
			Output:   25.00,
			Legacy:   false,
		},
		{
			ID:       "claude-sonnet-5",
			Alias:    "claude-sonnet-5",
			Provider: "Anthropic",
			Note:     "Sonnet 5 - best combination of speed and intelligence, near-Opus coding/agentic quality (intro pricing $2/$10 per MTok through 2026-08-31)",
			Input:    3.00,
			Output:   15.00,
			Legacy:   false,
		},
		{
			ID:       "claude-sonnet-4-6-20260115",
			Alias:    "claude-sonnet-4-6",
			Provider: "Anthropic",
			Note:     "Sonnet 4.6 - best combination of speed and intelligence",
			Input:    3.00,
			Output:   15.00,
			Legacy:   false,
		},
		// Claude 4.5 models
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
			ID:       "claude-sonnet-4-5-20250929",
			Alias:    "claude-sonnet-4-5",
			Provider: "Anthropic",
			Note:     "Sonnet 4.5 - best balance of speed and intelligence",
			Input:    3.00,
			Output:   15.00,
			Legacy:   true,
		},
		{
			ID:       "claude-opus-4-5-20251101",
			Alias:    "claude-opus-4-5",
			Provider: "Anthropic",
			Note:     "Opus 4.5 - maximum intelligence",
			Input:    5.00,
			Output:   25.00,
			Legacy:   true,
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
// Returns default Sonnet pricing if the model is not found. Prefer
// GetPricingOK when the caller needs to distinguish a table hit from the
// fallback (e.g. to flag a cost as estimated/unpriced).
func GetPricing(model string) (input, output float64) {
	input, output, _ = GetPricingOK(model)
	return input, output
}

// GetPricingOK returns input and output price per million tokens for a model
// plus an ok flag reporting whether the price came from the model table (true)
// or the 3/15 default fallback (false). Lookup is three-tier:
//
//  1. Exact: resolve the alias, then match a table entry's full ID. This is
//     the authoritative path for every currently-known model.
//  2. Family substring: match when the (alias-resolved) model string contains a
//     table entry's alias, longest alias winning. This keeps pricing resilient
//     to newer dated snapshots not yet in the table — e.g. a hypothetical
//     "claude-sonnet-4-6-20260901" still prices as the "claude-sonnet-4-6"
//     family rather than silently falling to the default. Longest-alias-wins
//     ensures "...-sonnet-4-6-..." binds to "claude-sonnet-4-6", not the
//     shorter "claude-sonnet-4".
//  3. Fallback: Sonnet 3/15 as a safe middle-ground, ok=false.
func GetPricingOK(model string) (input, output float64, ok bool) {
	resolved := ResolveAlias(model)

	// Tier 1: exact full-ID match.
	for _, m := range Models() {
		if m.ID == resolved {
			return m.Input, m.Output, true
		}
	}

	// Tier 2: family substring, longest matching alias wins.
	bestLen := 0
	for _, m := range Models() {
		if m.Alias == "" {
			continue
		}
		if strings.Contains(resolved, m.Alias) && len(m.Alias) > bestLen {
			bestLen = len(m.Alias)
			input, output = m.Input, m.Output
			ok = true
		}
	}
	if ok {
		return input, output, true
	}

	// Tier 3: default fallback.
	return 3.00, 15.00, false
}
