package logging

import (
	"math"
	"testing"

	"github.com/grovetools/grove-anthropic/pkg/models"
)

const priceEpsilon = 1e-9

func floatEq(a, b float64) bool { return math.Abs(a-b) < priceEpsilon }

// TestModelPrices covers the three-tier lookup (exact → family-substring →
// default fallback) plus the Sonnet long-context override.
func TestModelPrices(t *testing.T) {
	const belowThreshold = int64(1000)
	aboveThreshold := models.LongContextThreshold + 1

	cases := []struct {
		name        string
		model       string
		inputTokens int64
		wantInput   float64
		wantOutput  float64
		wantKnown   bool
	}{
		// Tier 1: exact table IDs / aliases.
		{"fable-5 exact", "claude-fable-5", belowThreshold, 10.00, 50.00, true},
		{"opus-4-8 exact", "claude-opus-4-8", belowThreshold, 5.00, 25.00, true},
		{"sonnet-4-6 exact id", "claude-sonnet-4-6-20260115", belowThreshold, 3.00, 15.00, true},
		{"haiku-4-5 alias", "claude-haiku-4-5", belowThreshold, 1.00, 5.00, true},

		// Tier 2: dated snapshots not in the table resolve by family.
		{"future sonnet-4-6 snapshot", "claude-sonnet-4-6-20991231", belowThreshold, 3.00, 15.00, true},
		{"future opus-4-8 snapshot", "claude-opus-4-8-20991231", belowThreshold, 5.00, 25.00, true},
		{"future fable-5 snapshot", "claude-fable-5-20991231", belowThreshold, 10.00, 50.00, true},

		// Tier 3: unknown model → default fallback, not known.
		{"unknown model", "gpt-4o", belowThreshold, 3.00, 15.00, false},
		{"empty model", "", belowThreshold, 3.00, 15.00, false},

		// Sonnet long-context override applies on top of the table rate.
		{"sonnet-4-6 long context", "claude-sonnet-4-6", aboveThreshold, 6.00, 22.50, true},
		{"future sonnet snapshot long context", "claude-sonnet-4-6-20991231", aboveThreshold, 6.00, 22.50, true},
		// Non-Sonnet models are unaffected by the input-token count.
		{"opus-4-8 large input", "claude-opus-4-8", aboveThreshold, 5.00, 25.00, true},
		// Sonnet 5 prices flat ($3/$15, 1M window) — the sonnet-4 long-context
		// tier must not apply to it.
		{"sonnet-5 long context stays flat", "claude-sonnet-5", aboveThreshold, 3.00, 15.00, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out, known := modelPrices(tc.model, tc.inputTokens)
			if !floatEq(in, tc.wantInput) || !floatEq(out, tc.wantOutput) {
				t.Errorf("modelPrices(%q, %d) = (%v, %v); want (%v, %v)",
					tc.model, tc.inputTokens, in, out, tc.wantInput, tc.wantOutput)
			}
			if known != tc.wantKnown {
				t.Errorf("modelPrices(%q) known = %v; want %v", tc.model, known, tc.wantKnown)
			}
		})
	}
}

// TestEstimateCostWithCacheOK verifies the cache multipliers (1.25x write,
// 0.10x read) and the known-pricing flag.
func TestEstimateCostWithCacheOK(t *testing.T) {
	// haiku-4-5: input 1.00 / output 5.00 per Mtok. 1M of each class:
	//   input 1.00 + cacheWrite 1.25 + cacheRead 0.10 + output 5.00 = 7.35
	cost, known := EstimateCostWithCacheOK("claude-haiku-4-5", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !floatEq(cost, 7.35) {
		t.Errorf("cache-heavy cost = %v; want 7.35", cost)
	}
	if !known {
		t.Errorf("haiku-4-5 should be known-priced")
	}

	// Unknown model still computes a cost (fallback rate) but flags it.
	_, known = EstimateCostWithCacheOK("gpt-4o", 1000, 1000, 0, 0)
	if known {
		t.Errorf("unknown model should report knownPricing=false")
	}
}

// TestPricingDriftGuard is the guard that would have caught the fable-5
// under-bill: every non-legacy model in the table must price through modelPrices
// at its table rate, both for its exact ID and for a synthetic dated snapshot of
// its alias (exercising the family-substring tier). Sonnet long-context is
// avoided by pricing a small input.
func TestPricingDriftGuard(t *testing.T) {
	const smallInput = int64(1000)
	for _, m := range models.Models() {
		if m.Legacy {
			continue
		}
		// Exact ID.
		in, out, known := modelPrices(m.ID, smallInput)
		if !floatEq(in, m.Input) || !floatEq(out, m.Output) || !known {
			t.Errorf("exact %q: got (%v, %v, known=%v); table has (%v, %v)",
				m.ID, in, out, known, m.Input, m.Output)
		}
		// Synthetic dated snapshot of the alias, e.g. claude-sonnet-4-6-20991231,
		// exercising the family-substring tier. A small input keeps Sonnet exact
		// (below the long-context override), so no per-family special-case here.
		if m.Alias != "" {
			snapshot := m.Alias + "-20991231"
			in, out, known = modelPrices(snapshot, smallInput)
			if !floatEq(in, m.Input) || !floatEq(out, m.Output) || !known {
				t.Errorf("snapshot %q: got (%v, %v, known=%v); table has (%v, %v)",
					snapshot, in, out, known, m.Input, m.Output)
			}
		}
	}
}

// TestEstimateCostWithCacheSplitOK verifies the TTL-split write multipliers
// (1.25x for 5m, 2.0x for 1h — spec 19 D9) and that the flat-total wrapper
// prices everything at the 5m rate.
func TestEstimateCostWithCacheSplitOK(t *testing.T) {
	// haiku-4-5: input 1.00 / output 5.00 per Mtok. 1M of each class:
	//   input 1.00 + write5m 1.25 + write1h 2.00 + cacheRead 0.10 + output 5.00 = 9.35
	cost, known := EstimateCostWithCacheSplitOK("claude-haiku-4-5", 1_000_000, 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !floatEq(cost, 9.35) {
		t.Errorf("split cost = %v; want 9.35", cost)
	}
	if !known {
		t.Errorf("haiku-4-5 should be known-priced")
	}

	// All-1h writes at 2.0x: 1.00 input + 2.00 write1h = 3.00.
	cost, _ = EstimateCostWithCacheSplitOK("claude-haiku-4-5", 1_000_000, 0, 0, 1_000_000, 0)
	if !floatEq(cost, 3.00) {
		t.Errorf("1h-write cost = %v; want 3.00", cost)
	}

	// The flat wrapper must equal the split call with everything in the 5m bucket.
	flat, _ := EstimateCostWithCacheOK("claude-haiku-4-5", 10, 20, 30, 40)
	split, _ := EstimateCostWithCacheSplitOK("claude-haiku-4-5", 10, 20, 30, 0, 40)
	if !floatEq(flat, split) {
		t.Errorf("EstimateCostWithCacheOK (%v) != all-5m split (%v)", flat, split)
	}
}
