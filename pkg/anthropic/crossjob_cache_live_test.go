package anthropic

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCrossJobCachePrefixSharingLive is the P0 live probe for spec
// plans/oracle-plays/19-spec-cache-fixes.md — it gates decision D8
// (cross-job cache lineage).
//
// Hypothesis under test: Anthropic's prompt cache is org-scoped and
// content-keyed, NOT conversation- or client-scoped. Two *separate*
// GenerateContent calls — made through two fresh clients, simulating two
// independent flow jobs/processes — that share a byte-identical document
// prefix should see call 2 cache-READ what call 1 cache-WROTE, even though
// nothing but the org + model + prefix bytes connects them.
//
// Shape of the probe:
//
//	call 1 (fresh client): [shared doc ★BP][prompt A]  → expect cache_creation ≈ doc size
//	call 2 (fresh client): [shared doc ★BP][prompt B]  → expect cache_read ≥ call 1's cache_creation − ε
//
// The shared document is salted with a unique-per-run string so the run
// always starts cold (no pre-existing org cache entry from a previous run).
// The tails (prompt text) differ between the calls, so a hit cannot be
// explained by whole-request identity. Both calls happen back to back, well
// within the default 5m TTL. Note the documents are re-uploaded to the Files
// API on each call (fresh file IDs) — matching is content-based, so this must
// not matter (verified live in job 16).
//
// Gated on GROVE_ANTHROPIC_LIVE_CACHE_TEST=1; `go test ./...` skips it. The
// API key resolves via config.ResolveAPIKey (grove keys.toml), same as the
// pinned-cache live test. To run:
//
//	GROVE_ANTHROPIC_LIVE_CACHE_TEST=1 \
//	  go test ./pkg/anthropic -run TestCrossJobCachePrefixSharingLive -v -count=1
//
// Model: defaults to claude-haiku-4-5 (cheap). Haiku 4.5's minimum cacheable
// prefix is 4096 tokens, the largest floor of any current model, so the
// shared document is padded to ~16k tokens — safely cacheable everywhere.
// Override with GROVE_ANTHROPIC_LIVE_MODEL.
//
// Outcome semantics for the spec:
//   - PASS  → D8 stands: cross-job lineage inherits the parent's cache.
//   - FAIL (assertion) → D8 degrades to per-chat lineage only.
//   - Skip/Fatal on environment (no key, no network) → probe COULD-NOT-RUN;
//     rerun in a working environment before deciding D8.
func TestCrossJobCachePrefixSharingLive(t *testing.T) {
	if os.Getenv("GROVE_ANTHROPIC_LIVE_CACHE_TEST") != "1" {
		t.Skip("set GROVE_ANTHROPIC_LIVE_CACHE_TEST=1 to run the cross-job cache live probe")
	}

	dir := t.TempDir()

	// Unique-per-run salt guarantees a cold start: no prior run (or other org
	// traffic) can have cached this exact byte sequence.
	salt := fmt.Sprintf("probe-salt-%d", time.Now().UnixNano())

	// ~16k tokens of deterministic filler, salted. This is the shared prefix
	// both "jobs" upload as their (identical) stable document.
	shared := writeFile(t, dir, "shared.md",
		"# Cross-job shared context ("+salt+")\n"+
			strings.Repeat("shared prefix filler line "+salt+"\n", 4000))

	model := os.Getenv("GROVE_ANTHROPIC_LIVE_MODEL")
	if model == "" {
		model = "claude-haiku-4-5"
	}

	ctx := context.Background()

	// runJob simulates one independent job: a fresh RequestRunner, and — because
	// RunWithUsage constructs its own Client via NewClient/ResolveAPIKey on
	// every call — a fresh underlying Anthropic client too. Nothing is shared
	// between the two invocations except the org's API key and the bytes of
	// the shared document.
	runJob := func(label, prompt string) *UsageResult {
		t.Helper()
		runner := NewRequestRunner()
		_, u, err := runner.RunWithUsage(ctx, RequestOptions{
			Model:           model,
			Prompt:          prompt,
			WorkDir:         dir,
			ColdContextFile: shared, // the byte-identical stable prefix
			MaxTokens:       64,
		})
		if err != nil {
			// Distinguish environment failures (no key / no network) from
			// probe failures: neither is a D8 verdict.
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
		t.Logf("%s: cache_creation=%d cache_read=%d input=%d output=%d model=%s",
			label, u.CacheCreationTokens, u.CacheReadTokens, u.InputTokens, u.OutputTokens, u.Model)
		return u
	}

	// Job 1: cold — establishes the org cache entry for the salted prefix.
	u1 := runJob("job1", "Job 1: acknowledge the shared documents in one short sentence.")

	// Job 2: fresh client, same document prefix, DIFFERENT tail prompt.
	u2 := runJob("job2", "Job 2: name one phrase that recurs in the shared documents, in one short sentence.")

	// Sanity: job 1 must actually have written the prefix. If it didn't, the
	// probe setup is broken (prefix below the model's cacheable minimum, or
	// caching disabled) and neither PASS nor FAIL is meaningful.
	if u1.CacheCreationTokens == 0 {
		t.Fatalf("probe setup broken: job 1 wrote nothing to cache (cache_creation=0) — "+
			"prefix may be below the model's minimum cacheable size for %s", model)
	}

	// The D8 gate: job 2 must read (at ~0.1x) what job 1 wrote. Allow a small
	// epsilon for token-accounting jitter around the breakpoint.
	const epsilon = 50
	if u2.CacheReadTokens < u1.CacheCreationTokens-epsilon {
		t.Errorf("PROBE FAIL: cross-job prefix sharing NOT observed: "+
			"job2 cache_read=%d < job1 cache_creation=%d - %d — D8 degrades to per-chat lineage",
			u2.CacheReadTokens, u1.CacheCreationTokens, epsilon)
	} else {
		t.Logf("PROBE PASS: job2 cache_read=%d >= job1 cache_creation=%d - %d — cross-job prefix sharing confirmed (D8 stands)",
			u2.CacheReadTokens, u1.CacheCreationTokens, epsilon)
	}
}
