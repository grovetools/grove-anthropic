package anthropic

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
// file, plus pinned and caller context files.
type legacyFixture struct {
	workDir  string
	claudeMD string
	cold     string
	hot      string
	pinned   []string
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
		pinned: []string{
			mkFile(t, dir, "pin1.md", "pin one"),
			mkFile(t, dir, "pin2.md", "pin two"),
		},
		contexts: []string{
			mkFile(t, dir, "extra.md", "extra context"),
		},
	}
}

func TestAssembleContextRegions(t *testing.T) {
	t.Run("legacy full ordering: cold, CLAUDE.md, pinned, hot, context", func(t *testing.T) {
		fx := newLegacyFixture(t)
		regions := assembleContextRegions(RequestOptions{
			PinnedFiles:  fx.pinned,
			ContextFiles: fx.contexts,
		}, fx.workDir, fx.hot, fx.cold)

		wantFiles := []string{fx.cold, fx.claudeMD, fx.pinned[0], fx.pinned[1], fx.hot, fx.contexts[0]}
		if !reflect.DeepEqual(regions.Files, wantFiles) {
			t.Errorf("Files = %v, want %v", regions.Files, wantFiles)
		}
		if regions.Layout != CacheLayoutLegacy {
			t.Errorf("Layout = %q, want %q", regions.Layout, CacheLayoutLegacy)
		}
		if regions.StableCount != 2 || regions.PinnedCount != 2 {
			t.Errorf("StableCount/PinnedCount = %d/%d, want 2/2", regions.StableCount, regions.PinnedCount)
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
		if regions.StableCount != 0 || regions.PinnedCount != 0 {
			t.Errorf("StableCount/PinnedCount = %d/%d, want 0/0", regions.StableCount, regions.PinnedCount)
		}
	})

	t.Run("legacy dedup precedence: stable > pinned > volatile", func(t *testing.T) {
		fx := newLegacyFixture(t)
		regions := assembleContextRegions(RequestOptions{
			// CLAUDE.md also passed as a pin, pin1 also passed as a context
			// file: each stays in its earliest (most stable) channel.
			PinnedFiles:  []string{fx.claudeMD, fx.pinned[0]},
			ContextFiles: []string{fx.pinned[0], fx.contexts[0]},
		}, fx.workDir, fx.hot, fx.cold)

		wantFiles := []string{fx.cold, fx.claudeMD, fx.pinned[0], fx.hot, fx.contexts[0]}
		if !reflect.DeepEqual(regions.Files, wantFiles) {
			t.Errorf("Files = %v, want %v", regions.Files, wantFiles)
		}
		if regions.StableCount != 2 || regions.PinnedCount != 1 {
			t.Errorf("StableCount/PinnedCount = %d/%d, want 2/1", regions.StableCount, regions.PinnedCount)
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
			PinnedFiles:  fx.pinned, // deprecated: must be ignored under ladder
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
		if regions.StableCount != 0 || regions.PinnedCount != 0 {
			t.Errorf("StableCount/PinnedCount = %d/%d, want 0/0 under ladder", regions.StableCount, regions.PinnedCount)
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
		got := assembleContextRegions(RequestOptions{PinnedFiles: fx.pinned, ContextFiles: fx.contexts}, fx.workDir, fx.hot, fx.cold)
		want := assembleContextRegions(RequestOptions{CacheLayout: CacheLayoutLegacy, PinnedFiles: fx.pinned, ContextFiles: fx.contexts}, fx.workDir, fx.hot, fx.cold)
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
