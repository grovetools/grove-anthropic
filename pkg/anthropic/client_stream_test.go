package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// streamItems is a small builder for the stream layout's ordered item list.
func layerItem(path string) StreamItem   { return StreamItem{Kind: RequestBlockLayer, Path: path} }
func contextItem(path string) StreamItem { return StreamItem{Kind: RequestBlockContext, Path: path} }
func historyItem(text string) StreamItem { return StreamItem{Kind: RequestBlockHistory, Text: text} }

// TestStreamLadderByteIdentity is the migration guarantee AND the ladder
// non-regression guard in one (spec 27 §0): the SAME inputs — layer docs,
// context docs, history blocks, prompt, system — assembled through the ladder
// fields versus an equivalent head-anchored Stream must produce byte-identical
// buildMessageParams output (same block sequence, same cache_control
// positions) and identical DescribeRequest plans. Per A1/the change list the
// inputs MUST include context files, which is what makes the equivalence
// unconditional.
func TestStreamLadderByteIdentity(t *testing.T) {
	const (
		model  = "claude-test"
		prompt = "the current volatile turn"
		system = "you are the oracle"
		mt     = int64(4096)
	)
	layers := []string{"00-base.xml", "01-delta.xml"}
	contexts := []string{"include-a.md", "include-b.md"}
	history := []string{"<turn>q1/a1</turn>", "<turn>q2/a2</turn>", "<turn>q3/a3</turn>"}
	// Uploaded doc IDs are shared by both layouts: 2 layers + 2 context docs,
	// in that order (stream uploads only Path-bearing items in item order).
	fileIDs := []string{"id-l0", "id-l1", "id-c0", "id-c1"}

	for _, ttl := range []string{"", CacheTTL5m, CacheTTL1h} {
		t.Run("ttl="+ttl, func(t *testing.T) {
			ladderReq := GenerateRequest{
				Model: model, Prompt: prompt, SystemPrompt: system, MaxTokens: mt, CacheTTL: ttl,
				Regions: ContextRegions{
					Layout:     CacheLayoutLadder,
					Files:      append(append([]string{}, layers...), contexts...),
					LayerCount: len(layers),
				},
				HistoryBlocks: history,
			}

			// Stream-at-head: every layer at the head, context docs after the
			// last layer, then the exchanges. HeadAnchor pins BP2 to the last
			// layer (index 1).
			items := []StreamItem{
				layerItem(layers[0]), layerItem(layers[1]),
				contextItem(contexts[0]), contextItem(contexts[1]),
				historyItem(history[0]), historyItem(history[1]), historyItem(history[2]),
			}
			streamReq := GenerateRequest{
				Model: model, Prompt: prompt, SystemPrompt: system, MaxTokens: mt, CacheTTL: ttl,
				Regions: ContextRegions{Layout: CacheLayoutStream, Items: items, HeadAnchor: 1},
			}

			ladderJSON, err := json.Marshal(buildMessageParams(ladderReq, fileIDs))
			if err != nil {
				t.Fatalf("marshal ladder params: %v", err)
			}
			streamJSON, err := json.Marshal(buildMessageParams(streamReq, fileIDs))
			if err != nil {
				t.Fatalf("marshal stream params: %v", err)
			}
			if !bytes.Equal(ladderJSON, streamJSON) {
				t.Errorf("stream-at-head bytes diverge from ladder.\nladder: %s\nstream: %s", ladderJSON, streamJSON)
			}

			// DescribeRequest must agree too: identical (Kind, Path, Breakpoint,
			// TTL) sequence for the two layouts.
			ladderPlan, err := DescribeRequest(RequestOptions{
				Model: model, Prompt: prompt, SystemPrompt: system, CacheTTL: ttl,
				CacheLayout:   CacheLayoutLadder,
				LayerFiles:    layers,
				ContextFiles:  contexts,
				HistoryBlocks: history,
			})
			if err != nil {
				t.Fatalf("DescribeRequest ladder: %v", err)
			}
			streamPlan, err := DescribeRequest(RequestOptions{
				Model: model, Prompt: prompt, SystemPrompt: system, CacheTTL: ttl,
				CacheLayout: CacheLayoutStream,
				Stream:      items,
			})
			if err != nil {
				t.Fatalf("DescribeRequest stream: %v", err)
			}
			if !equalPlans(ladderPlan, streamPlan) {
				t.Errorf("DescribeRequest plans diverge.\nladder: %+v\nstream: %+v", ladderPlan, streamPlan)
			}
		})
	}
}

func equalPlans(a, b []RequestPlanEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestStreamComputeBreakpoints pins the 3-breakpoint placement across the
// three characteristic turn shapes (spec 27 §1): turn 1 (no history — BP2 and
// BP3 coincide on the last layer and dedupe; trailing context carries none),
// a steady turn (BP3 on the last history item), and a widening turn (a layer
// interleaved after the last history item carries BP3).
func TestStreamComputeBreakpoints(t *testing.T) {
	tests := []struct {
		name       string
		items      []StreamItem
		headAnchor int
		hasSystem  bool
		wantSystem bool
		wantDocs   []int
	}{
		{
			name:       "turn 1 no history: BP2==BP3 on last layer, trailing context none",
			items:      []StreamItem{layerItem("l0"), layerItem("l1"), contextItem("c0")},
			headAnchor: 1, hasSystem: true, wantSystem: true, wantDocs: []int{1},
		},
		{
			name:       "steady turn: BP3 on last history item",
			items:      []StreamItem{layerItem("l0"), layerItem("l1"), contextItem("c0"), historyItem("h0"), historyItem("h1")},
			headAnchor: 1, hasSystem: true, wantSystem: true, wantDocs: []int{1, 4},
		},
		{
			name:       "widening turn: BP3 on a trailing layer after last history",
			items:      []StreamItem{layerItem("l0"), layerItem("l1"), contextItem("c0"), historyItem("h0"), historyItem("h1"), layerItem("l2")},
			headAnchor: 1, hasSystem: true, wantSystem: true, wantDocs: []int{1, 5},
		},
		{
			name:       "no system: BP1 unset",
			items:      []StreamItem{layerItem("l0"), historyItem("h0")},
			headAnchor: 0, hasSystem: false, wantSystem: false, wantDocs: []int{0, 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			regions := ContextRegions{Layout: CacheLayoutStream, Items: tt.items, HeadAnchor: tt.headAnchor}
			plan := computeBreakpoints(regions, tt.hasSystem, false, false)
			if plan.System != tt.wantSystem {
				t.Errorf("System = %v, want %v", plan.System, tt.wantSystem)
			}
			if plan.History {
				t.Errorf("stream must never set the History breakpoint bool")
			}
			if !equalInts(indices(plan.Docs), tt.wantDocs) {
				t.Errorf("Docs = %v, want %v", indices(plan.Docs), tt.wantDocs)
			}
			if plan.count() > 3 {
				t.Errorf("stream plan uses %d breakpoints, want ≤3 (one spare)", plan.count())
			}
		})
	}

	// noCache kills every breakpoint under stream, same as the other layouts.
	regions := ContextRegions{Layout: CacheLayoutStream, Items: []StreamItem{layerItem("l0"), historyItem("h0")}, HeadAnchor: 0}
	plan := computeBreakpoints(regions, true, false, true)
	if plan.count() != 0 {
		t.Errorf("noCache stream plan = %d breakpoints, want 0", plan.count())
	}
}

// TestStreamPrefixExtension simulates turns 1..4 of one chat (append the prior
// exchange each turn, occasionally interleaving a widened layer) and asserts
// the append-only invariant: turn N−1's stream items are a strict prefix of
// turn N's (by Kind/Path/Text), and DescribeRequest's non-turn entries track
// them by (Kind, Path). Append-only-ness IS the cache guarantee.
func TestStreamPrefixExtension(t *testing.T) {
	// Head region: 2 layers + 1 context, fixed across turns.
	head := []StreamItem{layerItem("00-base.xml"), layerItem("01-inherited.xml"), contextItem("include.md")}

	// Each turn appends the previous exchange; turn 3 also interleaves a
	// widened layer right after exchange 2.
	turns := [][]StreamItem{
		clone(head), // turn 1: nothing stored yet
		append(clone(head), historyItem("<turn>e1</turn>")),
		append(clone(head), historyItem("<turn>e1</turn>"), historyItem("<turn>e2</turn>"), layerItem("02-widen.xml")),
		append(clone(head), historyItem("<turn>e1</turn>"), historyItem("<turn>e2</turn>"), layerItem("02-widen.xml"), historyItem("<turn>e3</turn>")),
	}

	for i := 1; i < len(turns); i++ {
		prev, cur := turns[i-1], turns[i]
		if len(prev) > len(cur) {
			t.Fatalf("turn %d shorter than turn %d", i+1, i)
		}
		for j := range prev {
			if prev[j] != cur[j] {
				t.Errorf("turn %d item %d = %+v, breaks prefix of turn %d (%+v)", i, j, prev[j], i+1, cur[j])
			}
		}
	}

	// DescribeRequest per turn: drop the trailing `turn` entry and the leading
	// system entry, the rest must be a strict prefix turn-over-turn by (Kind, Path).
	describe := func(items []StreamItem) []RequestPlanEntry {
		entries, err := DescribeRequest(RequestOptions{
			Model: "m", Prompt: "vol", SystemPrompt: "sys", CacheTTL: CacheTTL1h,
			CacheLayout: CacheLayoutStream, Stream: items,
		})
		if err != nil {
			t.Fatalf("DescribeRequest: %v", err)
		}
		if len(entries) == 0 || entries[len(entries)-1].Kind != RequestBlockTurn {
			t.Fatalf("expected trailing turn entry, got %+v", entries)
		}
		return entries[:len(entries)-1] // drop turn
	}
	for i := 1; i < len(turns); i++ {
		prev, cur := describe(turns[i-1]), describe(turns[i])
		if len(prev) > len(cur) {
			t.Fatalf("describe turn %d longer than turn %d", i, i+1)
		}
		for j := range prev {
			if prev[j].Kind != cur[j].Kind || prev[j].Path != cur[j].Path {
				t.Errorf("describe turn %d entry %d (%s %s) breaks prefix of turn %d (%s %s)",
					i, j, prev[j].Kind, prev[j].Path, i+1, cur[j].Kind, cur[j].Path)
			}
		}
	}
}

func clone(s []StreamItem) []StreamItem { return append([]StreamItem{}, s...) }

// TestStreamValidateCacheOptions covers the stream-mode guards: split fields
// must be empty and every StreamItem kind must be recognized.
func TestStreamValidateCacheOptions(t *testing.T) {
	base := RequestOptions{CacheLayout: CacheLayoutStream, Stream: []StreamItem{layerItem("l0")}}
	if err := base.validateCacheOptions(); err != nil {
		t.Fatalf("valid stream options rejected: %v", err)
	}
	bad := base
	bad.LayerFiles = []string{"x"}
	if err := bad.validateCacheOptions(); err == nil {
		t.Error("stream with non-empty LayerFiles must be rejected")
	}
	bad = base
	bad.ContextFiles = []string{"x"}
	if err := bad.validateCacheOptions(); err == nil {
		t.Error("stream with non-empty ContextFiles must be rejected")
	}
	bad = base
	bad.Stream = []StreamItem{{Kind: "bogus", Path: "x"}}
	if err := bad.validateCacheOptions(); err == nil {
		t.Error("stream with an unknown StreamItem kind must be rejected")
	}
}

// TestStreamDocAfterTextBoundaryLive is the Phase-1 gate (spec 27 §0): a live
// smoke test that a cache breakpoint DOWNSTREAM of a document-after-text
// boundary reads the full prefix on the second run. No shipped code exercises
// a document block placed after a text block with a breakpoint below it, so
// stream must confirm the API caches across that boundary before flow builds
// on it.
//
// Shape (single message):
//
//	system★  [doc: head layer]  [text: exchange]  [doc★: widened layer]  [text: prompt]
//
// Run twice with the same head+exchange+widened-layer bytes (only the volatile
// prompt changes). Run 2's cache_read must cover the whole prefix through the
// second document, and its cache_creation must be a tiny tail — proving the
// breakpoint below the doc-after-text boundary matched.
//
// Gated on GROVE_ANTHROPIC_LIVE_CACHE_TEST=1; `go test ./...` skips it. To run:
//
//	GROVE_ANTHROPIC_LIVE_CACHE_TEST=1 \
//	  go test ./pkg/anthropic -run TestStreamDocAfterTextBoundaryLive -v -count=1
func TestStreamDocAfterTextBoundaryLive(t *testing.T) {
	if os.Getenv("GROVE_ANTHROPIC_LIVE_CACHE_TEST") != "1" {
		t.Skip("set GROVE_ANTHROPIC_LIVE_CACHE_TEST=1 to run the stream doc-after-text live smoke test")
	}

	dir := t.TempDir()
	salt := fmt.Sprintf("stream-salt-%d", time.Now().UnixNano())
	// Two salted layer documents, each padded past the largest cacheable-prefix
	// floor so both breakpoints are eligible.
	headLayer := writeFile(t, dir, "00-base.xml",
		"<layer n=\"0\" source=\"live-test\">\n<!-- "+salt+" -->\n"+
			strings.Repeat("head layer filler "+salt+"\n", 4000)+"</layer>\n")
	widenLayer := writeFile(t, dir, "01-widen.xml",
		"<layer n=\"1\" source=\"live-test\" after_turn=\"e1\">\n<!-- "+salt+" -->\n"+
			strings.Repeat("widen layer filler "+salt+"\n", 4000)+"</layer>\n")

	model := os.Getenv("GROVE_ANTHROPIC_LIVE_MODEL")
	if model == "" {
		model = "claude-haiku-4-5"
	}

	items := []StreamItem{
		{Kind: RequestBlockLayer, Path: headLayer},
		{Kind: RequestBlockHistory, Text: "<turn>e1: prior exchange " + salt + "</turn>"},
		{Kind: RequestBlockLayer, Path: widenLayer},
	}

	ctx := context.Background()
	runner := NewRequestRunner()
	runTurn := func(prompt string) *UsageResult {
		t.Helper()
		_, u, err := runner.RunWithUsage(ctx, RequestOptions{
			Model:        model,
			Prompt:       prompt,
			SystemPrompt: "you are the oracle " + salt,
			WorkDir:      dir,
			CacheLayout:  CacheLayoutStream,
			CacheTTL:     CacheTTL1h,
			Stream:       items,
		})
		if err != nil {
			t.Fatalf("turn: %v", err)
		}
		return u
	}

	first := runTurn("volatile prompt A")
	second := runTurn("volatile prompt B")
	if second.CacheReadTokens <= first.CacheCreationTokens/2 {
		t.Errorf("run 2 cache_read=%d did not cover the doc-after-text prefix (run 1 write=%d)",
			second.CacheReadTokens, first.CacheCreationTokens)
	}
	if second.CacheWrite1h+second.CacheWrite5m >= first.CacheCreationTokens/2 {
		t.Errorf("run 2 cache_creation=%d too large; the prefix through the second doc did not match",
			second.CacheWrite1h+second.CacheWrite5m)
	}
}
