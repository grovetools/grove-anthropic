package anthropic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPinnedContextCachingLive is a Phase-4 verification harness (SCAFFOLD).
//
// Server-side prompt caching cannot be asserted with mocks — the ground truth is
// the API's returned UsageResult.CacheReadTokens / CacheCreationTokens. This test
// exercises the real three-breakpoint layout across three turns and asserts the
// ledger moves the way the design predicts:
//
//	turn 1: establish the stable+volatile cache (no pinned file yet)
//	turn 2: introduce a pinned file — the stable prefix (BP1) still reads from
//	        cache; the new pinned bytes are a one-time cache_creation
//	turn 3: repeat turn 2's request — the pinned region (BP2) now reads from
//	        cache too; cache_creation is only the per-turn volatile/prompt bytes
//
// It is gated on GROVE_ANTHROPIC_LIVE_CACHE_TEST=1 AND a resolvable API key, so
// `go test ./...` skips it. It intentionally does NOT run in CI. To exercise it:
//
//	GROVE_ANTHROPIC_LIVE_CACHE_TEST=1 ANTHROPIC_API_KEY=… \
//	  go test ./pkg/anthropic -run TestPinnedContextCachingLive -v
//
// NOTE: the assertions below are written against the design and have not been
// run against the live API. The cacheable-prefix floor (≥1024/2048 tokens for
// current models) means the stable/pinned documents must be large enough to be
// cacheable at all — the helper pads them. Tune thresholds on first real run.
func TestPinnedContextCachingLive(t *testing.T) {
	if os.Getenv("GROVE_ANTHROPIC_LIVE_CACHE_TEST") != "1" {
		t.Skip("set GROVE_ANTHROPIC_LIVE_CACHE_TEST=1 to run the live pinned-cache test")
	}

	dir := t.TempDir()
	// Pad each document above the per-model cacheable-prefix floor so the
	// breakpoints are not server-side no-ops.
	stable := writeFile(t, dir, "stable.md", "# Stable context\n"+strings.Repeat("stable filler line\n", 4000))
	pinned := writeFile(t, dir, "pinned.md", "# Pinned reference\n"+strings.Repeat("pinned filler line\n", 4000))
	volatile := writeFile(t, dir, "volatile.md", "# Volatile\n"+strings.Repeat("volatile filler line\n", 200))

	runner := NewRequestRunner()
	model := os.Getenv("GROVE_ANTHROPIC_LIVE_MODEL")
	if model == "" {
		model = DefaultModel
	}

	newOpts := func(prompt string, pinnedFiles []string) RequestOptions {
		return RequestOptions{
			Model:           model,
			Prompt:          prompt,
			WorkDir:         dir,
			ColdContextFile: stable, // seeds the stable half deterministically
			ContextFiles:    []string{volatile},
			PinnedFiles:     pinnedFiles,
			MaxTokens:       64,
		}
	}

	ctx := context.Background()

	// Turn 1: no pinned file. Establishes the stable+volatile cache.
	_, u1, err := runner.RunWithUsage(ctx, newOpts("Turn 1: acknowledge.", nil))
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	t.Logf("turn1 read=%d creation=%d input=%d", u1.CacheReadTokens, u1.CacheCreationTokens, u1.InputTokens)

	// Turn 2: introduce the pinned file. Stable prefix should still read; the
	// pinned bytes are a one-time creation.
	_, u2, err := runner.RunWithUsage(ctx, newOpts("Turn 2: acknowledge.", []string{pinned}))
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	t.Logf("turn2 read=%d creation=%d input=%d", u2.CacheReadTokens, u2.CacheCreationTokens, u2.InputTokens)
	if u2.CacheReadTokens == 0 {
		t.Errorf("turn 2: expected the stable prefix to read from cache, got cache_read=0")
	}
	if u2.CacheCreationTokens == 0 {
		t.Errorf("turn 2: expected the new pinned bytes to be written to cache, got cache_creation=0")
	}

	// Turn 3: repeat turn 2 within TTL. Pinned region should now read too, so
	// cache_read grows and cache_creation shrinks toward volatile/prompt-only.
	_, u3, err := runner.RunWithUsage(ctx, newOpts("Turn 2: acknowledge.", []string{pinned}))
	if err != nil {
		t.Fatalf("turn 3: %v", err)
	}
	t.Logf("turn3 read=%d creation=%d input=%d", u3.CacheReadTokens, u3.CacheCreationTokens, u3.InputTokens)
	if u3.CacheReadTokens <= u2.CacheReadTokens {
		t.Errorf("turn 3: expected cache_read to grow after pinned region caches (t2=%d t3=%d)", u2.CacheReadTokens, u3.CacheReadTokens)
	}
	if u3.CacheCreationTokens >= u2.CacheCreationTokens {
		t.Errorf("turn 3: expected cache_creation to shrink once pinned region is cached (t2=%d t3=%d)", u2.CacheCreationTokens, u3.CacheCreationTokens)
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return p
}
