package anthropic

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLadderCacheTurnPairLive is the spec-19 P2 live verification of the
// ladder layout: a two-turn chat shape over the same LayerFiles bundle must
// (a) cache-read the whole bundle prefix on turn 2 and (b) write only the
// small per-turn tail — i.e. turn 2's cache_creation must be a tiny fraction
// of turn 1's bundle write. It also exercises the 1h TTL end to end and
// asserts the D9 usage split lands the writes in CacheWrite1h.
//
// Shape:
//
//	turn 1: ladder [layer-0 ★1h][prompt A] → cache_creation ≈ bundle, in CacheWrite1h
//	turn 2: ladder [layer-0 ★1h][prompt B] → cache_read ≥ turn 1's write − ε,
//	        cache_creation ≪ turn 1's write
//
// The layer document is salted per run so the probe always starts cold.
// Gated on GROVE_ANTHROPIC_LIVE_CACHE_TEST=1; `go test ./...` skips it. Use a
// cheap model (default claude-haiku-4-5; its 4096-token cacheable-prefix
// floor is the largest of any current model, so the bundle is padded well
// past it). To run:
//
//	GROVE_ANTHROPIC_LIVE_CACHE_TEST=1 \
//	  go test ./pkg/anthropic -run TestLadderCacheTurnPairLive -v -count=1
func TestLadderCacheTurnPairLive(t *testing.T) {
	if os.Getenv("GROVE_ANTHROPIC_LIVE_CACHE_TEST") != "1" {
		t.Skip("set GROVE_ANTHROPIC_LIVE_CACHE_TEST=1 to run the ladder cache live test")
	}

	dir := t.TempDir()
	salt := fmt.Sprintf("ladder-salt-%d", time.Now().UnixNano())

	// ~16k tokens of salted filler: the frozen layer-0 bundle both turns share.
	layer0 := writeFile(t, dir, "00-base.xml",
		"<layer n=\"0\" source=\"live-test\">\n<!-- "+salt+" -->\n"+
			strings.Repeat("layer zero filler line "+salt+"\n", 4000)+
			"</layer>\n")

	model := os.Getenv("GROVE_ANTHROPIC_LIVE_MODEL")
	if model == "" {
		model = "claude-haiku-4-5"
	}

	ctx := context.Background()
	runner := NewRequestRunner()

	runTurn := func(label, prompt string) *UsageResult {
		t.Helper()
		_, u, err := runner.RunWithUsage(ctx, RequestOptions{
			Model:       model,
			Prompt:      prompt,
			WorkDir:     dir,
			CacheLayout: CacheLayoutLadder,
			CacheTTL:    CacheTTL1h,
			LayerFiles:  []string{layer0},
			MaxTokens:   64,
		})
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "creating Anthropic client") ||
				strings.Contains(msg, "API key") || strings.Contains(msg, "api key") {
				t.Skipf("COULD-NOT-RUN (%s): API key not resolvable: %v", label, err)
			}
			t.Fatalf("COULD-NOT-RUN (%s): request failed for environment reasons: %v", label, err)
		}
		if u == nil {
			t.Fatalf("%s: nil usage result", label)
		}
		t.Logf("%s: cache_creation=%d (5m=%d 1h=%d) cache_read=%d input=%d output=%d",
			label, u.CacheCreationTokens, u.CacheWrite5m, u.CacheWrite1h, u.CacheReadTokens, u.InputTokens, u.OutputTokens)
		return u
	}

	// Turn 1: cold — writes the layer-0 prefix at the 1h premium.
	u1 := runTurn("turn1", "Turn 1: acknowledge the layer document in one short sentence.")
	if u1.CacheCreationTokens == 0 {
		t.Fatalf("probe setup broken: turn 1 wrote nothing to cache — bundle may be below %s's cacheable minimum", model)
	}
	// D9 usage split: a 1h-TTL request's writes must land in CacheWrite1h.
	if u1.CacheWrite1h == 0 {
		t.Errorf("turn 1: CacheWrite1h = 0 for a 1h-TTL request (flat=%d, 5m=%d) — usage split broken",
			u1.CacheCreationTokens, u1.CacheWrite5m)
	}

	// Turn 2: same layer prefix, different tail prompt.
	u2 := runTurn("turn2", "Turn 2: name one phrase that recurs in the layer document, in one short sentence.")

	// Prefix survival: turn 2 reads what turn 1 wrote.
	const epsilon = 50
	if u2.CacheReadTokens < u1.CacheCreationTokens-epsilon {
		t.Errorf("prefix did NOT survive: turn2 cache_read=%d < turn1 cache_creation=%d - %d",
			u2.CacheReadTokens, u1.CacheCreationTokens, epsilon)
	}

	// Second-turn write ≪ bundle size: only the tiny per-turn tail may write.
	if u2.CacheCreationTokens > u1.CacheCreationTokens/10 {
		t.Errorf("turn 2 rewrote too much: cache_creation=%d > 10%% of the bundle write (%d)",
			u2.CacheCreationTokens, u1.CacheCreationTokens)
	}
}
