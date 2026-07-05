package anthropic

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/grovetools/grove-anthropic/pkg/logging"
)

// mkFile writes a small file under dir and returns its path.
func mkFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// legacyFixture is a representative on-disk layout for legacy assembly tests:
// a workdir with CLAUDE.md, a non-empty cold-context file, a hot-context
// file, plus caller context files.
type legacyFixture struct {
	workDir  string
	claudeMD string
	cold     string
	hot      string
	contexts []string
}

func newLegacyFixture(t *testing.T) legacyFixture {
	t.Helper()
	dir := t.TempDir()
	return legacyFixture{
		workDir:  dir,
		claudeMD: mkFile(t, dir, "CLAUDE.md", "# instructions"),
		cold:     mkFile(t, dir, "cached-context.xml", `<context><cold-context files="2">cold bytes</cold-context></context>`),
		hot:      mkFile(t, dir, "context.xml", `<hot-context files="3">hot bytes</hot-context>`),
		contexts: []string{
			mkFile(t, dir, "extra.md", "extra context"),
		},
	}
}

func TestAssembleContextRegions(t *testing.T) {
	t.Run("legacy full ordering: cold, CLAUDE.md, hot, context", func(t *testing.T) {
		fx := newLegacyFixture(t)
		regions := assembleContextRegions(RequestOptions{
			ContextFiles: fx.contexts,
		}, fx.workDir, fx.hot, fx.cold)

		wantFiles := []string{fx.cold, fx.claudeMD, fx.hot, fx.contexts[0]}
		if !reflect.DeepEqual(regions.Files, wantFiles) {
			t.Errorf("Files = %v, want %v", regions.Files, wantFiles)
		}
		if regions.Layout != CacheLayoutLegacy {
			t.Errorf("Layout = %q, want %q", regions.Layout, CacheLayoutLegacy)
		}
		if regions.StableCount != 2 {
			t.Errorf("StableCount = %d, want 2", regions.StableCount)
		}
		if regions.LayerCount != 0 {
			t.Errorf("LayerCount = %d, want 0 under legacy", regions.LayerCount)
		}
	})

	t.Run("legacy empty cold-context stub is skipped", func(t *testing.T) {
		fx := newLegacyFixture(t)
		emptyCold := mkFile(t, fx.workDir, "empty-cold.xml", `<context><cold-context files="0"></cold-context></context>`)
		regions := assembleContextRegions(RequestOptions{}, fx.workDir, fx.hot, emptyCold)

		wantFiles := []string{fx.claudeMD, fx.hot}
		if !reflect.DeepEqual(regions.Files, wantFiles) {
			t.Errorf("Files = %v, want %v", regions.Files, wantFiles)
		}
		if regions.StableCount != 1 {
			t.Errorf("StableCount = %d, want 1 (CLAUDE.md only)", regions.StableCount)
		}
	})

	t.Run("legacy missing cold/CLAUDE.md/hot: only context files remain", func(t *testing.T) {
		dir := t.TempDir()
		extra := mkFile(t, dir, "extra.md", "x")
		regions := assembleContextRegions(RequestOptions{
			ContextFiles: []string{extra},
		}, dir, filepath.Join(dir, "no-hot.xml"), filepath.Join(dir, "no-cold.xml"))

		wantFiles := []string{extra}
		if !reflect.DeepEqual(regions.Files, wantFiles) {
			t.Errorf("Files = %v, want %v", regions.Files, wantFiles)
		}
		if regions.StableCount != 0 {
			t.Errorf("StableCount = %d, want 0", regions.StableCount)
		}
	})

	t.Run("legacy dedup precedence: stable > volatile", func(t *testing.T) {
		fx := newLegacyFixture(t)
		regions := assembleContextRegions(RequestOptions{
			// CLAUDE.md and the cold file also passed as context files: each
			// stays in its earliest (most stable) channel.
			ContextFiles: []string{fx.claudeMD, fx.cold, fx.contexts[0]},
		}, fx.workDir, fx.hot, fx.cold)

		wantFiles := []string{fx.cold, fx.claudeMD, fx.hot, fx.contexts[0]}
		if !reflect.DeepEqual(regions.Files, wantFiles) {
			t.Errorf("Files = %v, want %v", regions.Files, wantFiles)
		}
		if regions.StableCount != 2 {
			t.Errorf("StableCount = %d, want 2", regions.StableCount)
		}
	})

	t.Run("ladder ordering: layers then context files; no CLAUDE.md/hot/cold", func(t *testing.T) {
		fx := newLegacyFixture(t) // CLAUDE.md, hot, cold all exist on disk...
		layer0 := mkFile(t, fx.workDir, "00-base.xml", "layer zero")
		layer1 := mkFile(t, fx.workDir, "01-add.xml", "layer one")
		regions := assembleContextRegions(RequestOptions{
			CacheLayout:  CacheLayoutLadder,
			LayerFiles:   []string{layer0, layer1},
			ContextFiles: fx.contexts,
		}, fx.workDir, fx.hot, fx.cold) // ...and are still excluded (D6)

		wantFiles := []string{layer0, layer1, fx.contexts[0]}
		if !reflect.DeepEqual(regions.Files, wantFiles) {
			t.Errorf("Files = %v, want %v", regions.Files, wantFiles)
		}
		if regions.Layout != CacheLayoutLadder {
			t.Errorf("Layout = %q, want %q", regions.Layout, CacheLayoutLadder)
		}
		if regions.LayerCount != 2 {
			t.Errorf("LayerCount = %d, want 2", regions.LayerCount)
		}
		if regions.StableCount != 0 {
			t.Errorf("StableCount = %d, want 0 under ladder", regions.StableCount)
		}
	})

	t.Run("ladder dedup precedence: layers > context", func(t *testing.T) {
		dir := t.TempDir()
		layer0 := mkFile(t, dir, "00-base.xml", "layer zero")
		layer1 := mkFile(t, dir, "01-add.xml", "layer one")
		extra := mkFile(t, dir, "extra.md", "x")
		regions := assembleContextRegions(RequestOptions{
			CacheLayout:  CacheLayoutLadder,
			LayerFiles:   []string{layer0, layer1, layer0}, // dup within layers too
			ContextFiles: []string{layer1, extra},
		}, dir, "", "")

		wantFiles := []string{layer0, layer1, extra}
		if !reflect.DeepEqual(regions.Files, wantFiles) {
			t.Errorf("Files = %v, want %v", regions.Files, wantFiles)
		}
		if regions.LayerCount != 2 {
			t.Errorf("LayerCount = %d, want 2", regions.LayerCount)
		}
	})

	t.Run("ladder with no files at all", func(t *testing.T) {
		regions := assembleContextRegions(RequestOptions{CacheLayout: CacheLayoutLadder}, t.TempDir(), "", "")
		if len(regions.Files) != 0 || regions.LayerCount != 0 {
			t.Errorf("want empty regions, got %+v", regions)
		}
	})

	t.Run("empty CacheLayout defaults to legacy", func(t *testing.T) {
		fx := newLegacyFixture(t)
		got := assembleContextRegions(RequestOptions{ContextFiles: fx.contexts}, fx.workDir, fx.hot, fx.cold)
		want := assembleContextRegions(RequestOptions{CacheLayout: CacheLayoutLegacy, ContextFiles: fx.contexts}, fx.workDir, fx.hot, fx.cold)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("empty layout %+v != explicit legacy %+v", got, want)
		}
	})
}

func TestValidateCacheOptions(t *testing.T) {
	valid := []RequestOptions{
		{},
		{CacheLayout: CacheLayoutLegacy},
		{CacheLayout: CacheLayoutLadder, CacheTTL: CacheTTL1h},
		{CacheTTL: CacheTTL5m},
	}
	for _, o := range valid {
		if err := o.validateCacheOptions(); err != nil {
			t.Errorf("validateCacheOptions(%+v) = %v, want nil", o, err)
		}
	}
	invalid := []RequestOptions{
		{CacheLayout: "Ladder"},
		{CacheLayout: "stability-ladder"},
		{CacheTTL: "2h"},
		{CacheTTL: "5M"},
	}
	for _, o := range invalid {
		if err := o.validateCacheOptions(); err == nil {
			t.Errorf("validateCacheOptions(%+v) = nil, want error", o)
		}
	}
}

// fakeBetaUsage builds an SDK usage payload for split-extraction tests.
func fakeBetaUsage(input, output, flatCreation, read, w5m, w1h int64) *sdk.BetaUsage {
	u := &sdk.BetaUsage{
		InputTokens:              input,
		OutputTokens:             output,
		CacheCreationInputTokens: flatCreation,
		CacheReadInputTokens:     read,
	}
	u.CacheCreation.Ephemeral5mInputTokens = w5m
	u.CacheCreation.Ephemeral1hInputTokens = w1h
	return u
}

// TestSplitCacheWrites covers the 5m/1h extraction from the API's
// usage.cache_creation detail plus the flat-total fallback for responses that
// omit the split (spec 19 D9).
func TestSplitCacheWrites(t *testing.T) {
	tests := []struct {
		name           string
		usage          *sdk.BetaUsage
		requestTTL     string
		want5m, want1h int64
	}{
		{"split present: passed through", fakeBetaUsage(10, 5, 1000, 200, 400, 600), CacheTTL1h, 400, 600},
		{"split present, 5m only", fakeBetaUsage(10, 5, 700, 0, 700, 0), "", 700, 0},
		{"no split, request asked 1h: flat → 1h", fakeBetaUsage(10, 5, 900, 0, 0, 0), CacheTTL1h, 0, 900},
		{"no split, request asked 5m: flat → 5m", fakeBetaUsage(10, 5, 900, 0, 0, 0), CacheTTL5m, 900, 0},
		{"no split, empty TTL (API default 5m): flat → 5m", fakeBetaUsage(10, 5, 900, 0, 0, 0), "", 900, 0},
		{"nothing written", fakeBetaUsage(10, 5, 0, 300, 0, 0), CacheTTL1h, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got5m, got1h := splitCacheWrites(tt.usage, tt.requestTTL)
			if got5m != tt.want5m || got1h != tt.want1h {
				t.Errorf("splitCacheWrites() = (%d, %d), want (%d, %d)", got5m, got1h, tt.want5m, tt.want1h)
			}
		})
	}
}

// TestNewUsageResult asserts the UsageResult carries the split AND prices 1h
// writes at 2.0x the input rate (vs 1.25x for 5m) — the D9 ledger-honesty fix.
func TestNewUsageResult(t *testing.T) {
	const model = "claude-fable-5" // $10/MTok in, $50/MTok out (known pricing)
	u := fakeBetaUsage(100_000, 10_000, 1_000_000, 500_000, 400_000, 600_000)

	got := newUsageResult(model, u, CacheTTL1h)
	if got.CacheWrite5m != 400_000 || got.CacheWrite1h != 600_000 {
		t.Errorf("split = (%d, %d), want (400000, 600000)", got.CacheWrite5m, got.CacheWrite1h)
	}
	if got.CacheCreationTokens != 1_000_000 {
		t.Errorf("CacheCreationTokens = %d, want 1000000 (flat total kept)", got.CacheCreationTokens)
	}
	if !got.KnownPricing {
		t.Errorf("KnownPricing = false, want true for %s", model)
	}
	want := logging.EstimateCostWithCacheSplit(model, 100_000, 10_000, 400_000, 600_000, 500_000)
	if got.EstimatedCostUSD != want {
		t.Errorf("EstimatedCostUSD = %v, want %v", got.EstimatedCostUSD, want)
	}

	// The same tokens priced as all-5m must be strictly cheaper than the real
	// split — i.e. the 2x 1h premium is actually applied.
	all5m := logging.EstimateCostWithCacheSplit(model, 100_000, 10_000, 1_000_000, 0, 500_000)
	if got.EstimatedCostUSD <= all5m {
		t.Errorf("split cost %v not greater than all-5m cost %v — 1h premium not applied", got.EstimatedCostUSD, all5m)
	}
}

// TestDescribeRequestLadder pins the manifest-facing block plan for a ladder
// request: system → layers (breakpoint+TTL on the last) → context docs →
// one history entry per prior-turn block (breakpoint on the LAST only) →
// turn (never cached). Empty history elements are dropped (FilterHistoryBlocks).
func TestDescribeRequestLadder(t *testing.T) {
	dir := t.TempDir()
	layer0 := mkFile(t, dir, "00-base.xml", "layer zero")
	layer1 := mkFile(t, dir, "01-add.xml", "layer one")
	extra := mkFile(t, dir, "extra.md", "x")

	entries, err := DescribeRequest(RequestOptions{
		Model:         "claude-test",
		Prompt:        "turn K",
		SystemPrompt:  "template",
		HistoryBlocks: []string{"turn 1", "", "turn 2"}, // empty element dropped
		WorkDir:       dir,
		CacheLayout:   CacheLayoutLadder,
		CacheTTL:      CacheTTL1h,
		LayerFiles:    []string{layer0, layer1},
		ContextFiles:  []string{extra},
	})
	if err != nil {
		t.Fatalf("DescribeRequest: %v", err)
	}

	want := []RequestPlanEntry{
		{Kind: RequestBlockSystem, Breakpoint: true, TTL: CacheTTL1h},
		{Kind: RequestBlockLayer, Path: layer0},
		{Kind: RequestBlockLayer, Path: layer1, Breakpoint: true, TTL: CacheTTL1h},
		{Kind: RequestBlockContext, Path: extra},
		{Kind: RequestBlockHistory},
		{Kind: RequestBlockHistory, Breakpoint: true, TTL: CacheTTL1h},
		{Kind: RequestBlockTurn},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v\nwant %+v", entries, want)
	}

	// Breakpoint budget for the combined shape: system(1) + last-layer(2) +
	// last-history(3) = 3 of the API's 4, one spare (spec 19 D1/P4).
	bpCount := 0
	for _, e := range entries {
		if e.Breakpoint {
			bpCount++
		}
	}
	if bpCount != 3 {
		t.Errorf("combined ladder shape places %d breakpoints, want exactly 3", bpCount)
	}

	// No system/history (the P2 flow chat shape): single breakpoint on the
	// last layer; turn always last.
	entries, err = DescribeRequest(RequestOptions{
		Model:       "claude-test",
		Prompt:      "turn K",
		WorkDir:     dir,
		CacheLayout: CacheLayoutLadder,
		CacheTTL:    CacheTTL1h,
		LayerFiles:  []string{layer0, layer1},
	})
	if err != nil {
		t.Fatalf("DescribeRequest: %v", err)
	}
	want = []RequestPlanEntry{
		{Kind: RequestBlockLayer, Path: layer0},
		{Kind: RequestBlockLayer, Path: layer1, Breakpoint: true, TTL: CacheTTL1h},
		{Kind: RequestBlockTurn},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v\nwant %+v", entries, want)
	}

	// NoCache: zero breakpoints, no TTLs, same block order.
	entries, err = DescribeRequest(RequestOptions{
		Model:       "claude-test",
		Prompt:      "turn K",
		WorkDir:     dir,
		CacheLayout: CacheLayoutLadder,
		CacheTTL:    CacheTTL1h,
		NoCache:     true,
		LayerFiles:  []string{layer0, layer1},
	})
	if err != nil {
		t.Fatalf("DescribeRequest: %v", err)
	}
	for _, e := range entries {
		if e.Breakpoint || e.TTL != "" {
			t.Errorf("NoCache entry carries breakpoint/TTL: %+v", e)
		}
	}

	// Invalid options are rejected exactly like the live path.
	if _, err := DescribeRequest(RequestOptions{Prompt: "x", CacheTTL: "2h"}); err == nil {
		t.Error("DescribeRequest with invalid CacheTTL = nil error, want error")
	}
}
