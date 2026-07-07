package anthropic

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// withDummyKey sets a throwaway API key for the duration of a test so
// NewClient (called by newSharedPrefix) succeeds without a real credential.
// None of these tests hit the network.
func withDummyKey(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-dummy")
}

func TestNewSharedPrefix_DefaultsAndAliasResolution(t *testing.T) {
	withDummyKey(t)

	p, err := NewSharedPrefix("sys", []byte("hello context"), SharedPrefixOptions{
		Model: "claude-haiku-4-5", // alias
	})
	if err != nil {
		t.Fatalf("NewSharedPrefix: %v", err)
	}
	defer p.Close()

	if got, want := p.Model(), "claude-haiku-4-5-20251001"; got != want {
		t.Errorf("Model() = %q, want resolved %q", got, want)
	}
	if p.ttl != CacheTTL5m {
		t.Errorf("default TTL = %q, want %q", p.ttl, CacheTTL5m)
	}
	if p.maxTokens != DefaultFanoutMaxTokens {
		t.Errorf("default MaxTokens = %d, want %d", p.maxTokens, DefaultFanoutMaxTokens)
	}
	if len(p.tmpFiles) != 1 {
		t.Fatalf("expected 1 owned temp file, got %d", len(p.tmpFiles))
	}
	if _, err := os.Stat(p.tmpFiles[0]); err != nil {
		t.Errorf("prefix temp file should exist before Close: %v", err)
	}
}

func TestSharedPrefix_CloseRemovesOwnedTempFile(t *testing.T) {
	withDummyKey(t)

	p, err := NewSharedPrefix("", []byte("ctx"), SharedPrefixOptions{Model: "claude-haiku-4-5"})
	if err != nil {
		t.Fatalf("NewSharedPrefix: %v", err)
	}
	tmp := p.tmpFiles[0]
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("temp file should be removed after Close, stat err = %v", err)
	}
}

func TestNewSharedPrefix_InvalidOptions(t *testing.T) {
	withDummyKey(t)

	if _, err := NewSharedPrefix("", []byte("x"), SharedPrefixOptions{Model: ""}); err == nil {
		t.Error("expected error for empty model")
	}
	if _, err := NewSharedPrefix("", []byte("x"), SharedPrefixOptions{Model: "claude-haiku-4-5", TTL: "9m"}); err == nil {
		t.Error("expected error for invalid TTL")
	}
	if _, err := NewSharedPrefixFromFiles("", nil, SharedPrefixOptions{Model: "claude-haiku-4-5"}); err == nil {
		t.Error("expected error for empty context files")
	}
}

func TestSharedPrefix_BuildRequestLadderLayout(t *testing.T) {
	withDummyKey(t)

	files := []string{"/tmp/cold", "/tmp/claude.md", "/tmp/hot"}
	p, err := NewSharedPrefixFromFiles("system prompt", files, SharedPrefixOptions{
		Model: "claude-haiku-4-5",
		TTL:   CacheTTL1h,
	})
	if err != nil {
		t.Fatalf("NewSharedPrefixFromFiles: %v", err)
	}

	req := p.buildRequest("do the task")
	if req.Regions.Layout != CacheLayoutLadder {
		t.Errorf("layout = %q, want ladder", req.Regions.Layout)
	}
	if req.Regions.LayerCount != len(files) {
		t.Errorf("LayerCount = %d, want %d", req.Regions.LayerCount, len(files))
	}
	if req.CacheTTL != CacheTTL1h {
		t.Errorf("CacheTTL = %q, want %q", req.CacheTTL, CacheTTL1h)
	}
	if req.SystemPrompt != "system prompt" {
		t.Errorf("SystemPrompt = %q", req.SystemPrompt)
	}
	if req.Prompt != "do the task" {
		t.Errorf("Prompt = %q", req.Prompt)
	}

	// The assembled message must cache the whole prefix behind exactly the
	// system breakpoint + a single last-document breakpoint, with the volatile
	// task prompt left uncached at the tail.
	fakeIDs := []string{"file_0", "file_1", "file_2"}
	params := buildMessageParams(req, fakeIDs)

	if len(params.System) != 1 || params.System[0].CacheControl.Type == "" {
		t.Errorf("system block should carry a cache_control breakpoint (BP1)")
	}
	// 3 document blocks + 1 task text block.
	if len(params.Messages) != 1 {
		t.Fatalf("expected 1 user message, got %d", len(params.Messages))
	}
	blocks := params.Messages[0].Content
	if len(blocks) != len(files)+1 {
		t.Fatalf("expected %d content blocks, got %d", len(files)+1, len(blocks))
	}
	// Only the last document (index len(files)-1) is breakpointed; earlier docs
	// ride the prefix that the last breakpoint covers.
	if blocks[len(files)-1].OfDocument == nil || blocks[len(files)-1].OfDocument.CacheControl.Type == "" {
		t.Errorf("last prefix document should carry the prefix-end breakpoint")
	}
	if blocks[0].OfDocument == nil || blocks[0].OfDocument.CacheControl.Type != "" {
		t.Errorf("non-final prefix documents must not carry their own breakpoint")
	}
	// Tail block is the volatile task prompt, never cached.
	tail := blocks[len(blocks)-1]
	if tail.OfText == nil || tail.OfText.Text != "do the task" {
		t.Errorf("tail block should be the task text prompt, got %+v", tail)
	}
	if tail.OfText.CacheControl.Type != "" {
		t.Errorf("task prompt must not be cached")
	}
}

func TestSharedPrefix_RequestRejectsEmptyPrompt(t *testing.T) {
	withDummyKey(t)

	p, err := NewSharedPrefix("", []byte("ctx"), SharedPrefixOptions{Model: "claude-haiku-4-5"})
	if err != nil {
		t.Fatalf("NewSharedPrefix: %v", err)
	}
	defer p.Close()

	if _, _, err := p.Request(context.Background(), ""); err == nil {
		t.Error("expected error for empty task prompt")
	}
}

func TestWorkDirContextFiles_EmptyWhenNoContext(t *testing.T) {
	dir := t.TempDir()
	// A bare directory with no cx-generated context and no CLAUDE.md yields no
	// prefix documents.
	if files := WorkDirContextFiles(dir); len(files) != 0 {
		t.Errorf("expected no context files for empty dir, got %v", files)
	}

	// With a CLAUDE.md present, the legacy assembly includes it, proving the
	// helper mirrors the one-shot upload set.
	claude := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claude, []byte("# build"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	files := WorkDirContextFiles(dir)
	found := false
	for _, f := range files {
		if f == claude {
			found = true
		}
	}
	if !found {
		t.Errorf("expected CLAUDE.md in context files, got %v", files)
	}
}
