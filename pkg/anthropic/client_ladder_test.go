package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// ladderRegions builds a ladder ContextRegions with layerCount layer docs
// followed by extraCount plain context docs.
func ladderRegions(layerCount, extraCount int) ContextRegions {
	files := make([]string, 0, layerCount+extraCount)
	for i := 0; i < layerCount; i++ {
		files = append(files, fmt.Sprintf("layer-%02d.xml", i))
	}
	for i := 0; i < extraCount; i++ {
		files = append(files, fmt.Sprintf("extra-%02d.md", i))
	}
	return ContextRegions{Layout: CacheLayoutLadder, Files: files, LayerCount: layerCount}
}

func TestComputeBreakpointsLadder(t *testing.T) {
	tests := []struct {
		name        string
		regions     ContextRegions
		hasSystem   bool
		hasHistory  bool
		noCache     bool
		wantSystem  bool
		wantDocs    []int
		wantHistory bool
	}{
		{"layers only", ladderRegions(3, 0), false, false, false, false, []int{2}, false},
		{"single layer", ladderRegions(1, 0), false, false, false, false, []int{0}, false},
		{"layers + trailing context files get no own breakpoint", ladderRegions(2, 3), false, false, false, false, []int{1}, false},
		{"layers + history", ladderRegions(2, 0), false, true, false, false, []int{1}, true},
		{"layers + history + system (3 of 4 budget)", ladderRegions(2, 1), true, true, false, true, []int{1}, true},
		{"zero layers, system + history only", ladderRegions(0, 2), true, true, false, true, nil, true},
		{"zero layers, nothing cacheable", ladderRegions(0, 0), false, false, false, false, nil, false},
		{"noCache kills everything", ladderRegions(3, 1), true, true, true, false, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := computeBreakpoints(tt.regions, tt.hasSystem, tt.hasHistory, tt.noCache)
			if plan.System != tt.wantSystem {
				t.Errorf("System = %v, want %v", plan.System, tt.wantSystem)
			}
			if plan.History != tt.wantHistory {
				t.Errorf("History = %v, want %v", plan.History, tt.wantHistory)
			}
			got := indices(plan.Docs)
			want := tt.wantDocs
			if want == nil {
				want = []int{}
			}
			if !equalInts(got, want) {
				t.Errorf("Docs = %v, want %v", got, want)
			}
			if plan.count() > 4 {
				t.Errorf("plan uses %d breakpoints, exceeds the API budget of 4", plan.count())
			}
		})
	}
}

// TestComputeBreakpointsBudget sweeps both layouts across many shapes and
// asserts the API's 4-breakpoint budget is never exceeded (ladder should in
// fact never exceed 3, keeping one spare).
func TestComputeBreakpointsBudget(t *testing.T) {
	bools := []bool{false, true}
	for layers := 0; layers <= 6; layers++ {
		for extra := 0; extra <= 3; extra++ {
			for _, hasSystem := range bools {
				for _, hasHistory := range bools {
					plan := computeBreakpoints(ladderRegions(layers, extra), hasSystem, hasHistory, false)
					if plan.count() > 3 {
						t.Errorf("ladder layers=%d extra=%d sys=%v hist=%v: %d breakpoints (>3, no spare left)",
							layers, extra, hasSystem, hasHistory, plan.count())
					}
					for i := range plan.Docs {
						if i < 0 || i >= layers+extra {
							t.Errorf("ladder layers=%d extra=%d: out-of-range doc index %d", layers, extra, i)
						}
					}
				}
			}
		}
	}
	// Legacy budget: never marks system/history, docs capped at 2.
	for total := 0; total <= 6; total++ {
		for stable := 0; stable <= total; stable++ {
			regions := ContextRegions{Layout: CacheLayoutLegacy, Files: make([]string, total), StableCount: stable}
			plan := computeBreakpoints(regions, true, true, false)
			if plan.System || plan.History {
				t.Errorf("legacy total=%d: system/history breakpoints must never be set (got %v/%v)", total, plan.System, plan.History)
			}
			if plan.count() > 2 {
				t.Errorf("legacy total=%d stable=%d: %d breakpoints (>2)", total, stable, plan.count())
			}
		}
	}
}

// TestComputeBreakpointsLegacyMatchesHistorical pins the legacy plan to the
// historical (pinned-free) cacheBreakpointIndices output for representative
// shapes.
func TestComputeBreakpointsLegacyMatchesHistorical(t *testing.T) {
	shapes := []struct {
		total, stable int
		noCache       bool
	}{
		{5, 2, false},
		{6, 2, false},
		{4, 0, false},
		{6, 2, true},
		{0, 0, false},
	}
	for _, s := range shapes {
		regions := ContextRegions{Layout: CacheLayoutLegacy, Files: make([]string, s.total), StableCount: s.stable}
		plan := computeBreakpoints(regions, true, false, s.noCache)
		want := cacheBreakpointIndices(s.total, s.stable, s.noCache)
		if !equalInts(indices(plan.Docs), indices(want)) {
			t.Errorf("shape %+v: Docs = %v, want historical %v", s, indices(plan.Docs), indices(want))
		}
	}
}

func TestCacheControlParamTTL(t *testing.T) {
	tests := []struct {
		ttl      string
		wantJSON string
	}{
		// Empty TTL must serialize exactly like today's parameterless
		// NewBetaCacheControlEphemeralParam() — no ttl key at all (omitzero).
		{"", `{"type":"ephemeral"}`},
		{CacheTTL5m, `{"ttl":"5m","type":"ephemeral"}`},
		{CacheTTL1h, `{"ttl":"1h","type":"ephemeral"}`},
	}
	for _, tt := range tests {
		got, err := json.Marshal(cacheControlParam(tt.ttl))
		if err != nil {
			t.Fatalf("marshal cacheControlParam(%q): %v", tt.ttl, err)
		}
		if string(got) != tt.wantJSON {
			t.Errorf("cacheControlParam(%q) = %s, want %s", tt.ttl, got, tt.wantJSON)
		}
	}
}

// TestBuildMessageParamsTTLThreading asserts every breakpoint in a ladder
// request carries the request-wide TTL, and that no TTL appears when CacheTTL
// is empty.
func TestBuildMessageParamsTTLThreading(t *testing.T) {
	req := GenerateRequest{
		Model:         "claude-test",
		Prompt:        "current turn",
		SystemPrompt:  "you are the oracle",
		Regions:       ladderRegions(2, 1),
		HistoryPrefix: "turns 1..K-1",
		MaxTokens:     100,
		CacheTTL:      CacheTTL1h,
	}
	raw, err := json.Marshal(buildMessageParams(req, []string{"f0", "f1", "f2"}))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	s := string(raw)
	if got, want := strings.Count(s, `"cache_control"`), 3; got != want {
		t.Fatalf("cache_control count = %d, want %d (system + last layer + history); json: %s", got, want, s)
	}
	if got, want := strings.Count(s, `"ttl":"1h"`), 3; got != want {
		t.Errorf(`"ttl":"1h" count = %d, want %d — every breakpoint must carry the TTL; json: %s`, got, want, s)
	}

	// Same request without a TTL: breakpoints stay, ttl never serialized.
	req.CacheTTL = ""
	raw, err = json.Marshal(buildMessageParams(req, []string{"f0", "f1", "f2"}))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if strings.Contains(string(raw), `"ttl"`) {
		t.Errorf("empty CacheTTL serialized a ttl field: %s", raw)
	}
	if got, want := strings.Count(string(raw), `"cache_control"`), 3; got != want {
		t.Errorf("cache_control count = %d, want %d", got, want)
	}
}

// TestBuildMessageParamsLadderShape asserts the ladder block order: layer docs,
// context docs, history text block (with breakpoint), prompt text block
// (never cached); system param carries BP1.
func TestBuildMessageParamsLadderShape(t *testing.T) {
	req := GenerateRequest{
		Model:         "claude-test",
		Prompt:        "current turn",
		SystemPrompt:  "template",
		Regions:       ladderRegions(2, 1),
		HistoryPrefix: "history bytes",
		MaxTokens:     50,
		CacheTTL:      CacheTTL1h,
	}
	params := buildMessageParams(req, []string{"f0", "f1", "f2"})

	blocks := params.Messages[0].Content
	if len(blocks) != 5 {
		t.Fatalf("block count = %d, want 5 (3 docs + history + prompt)", len(blocks))
	}
	// Docs 0..2: only index 1 (last layer) has cache_control.
	for i := 0; i < 3; i++ {
		doc := blocks[i].OfDocument
		if doc == nil {
			t.Fatalf("block %d: not a document block", i)
		}
		hasCC := doc.CacheControl.Type == "ephemeral" && doc.CacheControl.TTL != ""
		if (i == 1) != hasCC {
			t.Errorf("doc block %d cache_control presence = %v, want %v", i, hasCC, i == 1)
		}
	}
	hist := blocks[3].OfText
	if hist == nil || hist.Text != "history bytes" {
		t.Fatalf("block 3: want history text block, got %+v", blocks[3])
	}
	if hist.CacheControl.TTL != anthropic.BetaCacheControlEphemeralTTLTTL1h {
		t.Errorf("history block TTL = %q, want 1h", hist.CacheControl.TTL)
	}
	prompt := blocks[4].OfText
	if prompt == nil || prompt.Text != "current turn" {
		t.Fatalf("block 4: want prompt text block, got %+v", blocks[4])
	}
	if prompt.CacheControl.Type != "" || prompt.CacheControl.TTL != "" {
		t.Errorf("prompt block must never carry cache_control, got %+v", prompt.CacheControl)
	}
	if len(params.System) != 1 {
		t.Fatalf("system blocks = %d, want 1", len(params.System))
	}
	if params.System[0].CacheControl.TTL != anthropic.BetaCacheControlEphemeralTTLTTL1h {
		t.Errorf("system block must carry BP1 with the TTL, got %+v", params.System[0].CacheControl)
	}

	// NoCache: zero breakpoints anywhere, history block still emitted.
	req.NoCache = true
	raw, err := json.Marshal(buildMessageParams(req, []string{"f0", "f1", "f2"}))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if strings.Contains(string(raw), "cache_control") {
		t.Errorf("NoCache request contains cache_control: %s", raw)
	}
}

// TestLegacyLayoutByteIdentity is the regression guard required by spec 19
// §8: for representative legacy inputs, buildMessageParams must serialize
// byte-identically to what the historical pinned-free code produced — same
// file order, same two-breakpoint set {stableCount-1, total-1}, cache_control
// params with no ttl. The reference is constructed inline exactly the way
// client.go built requests before the ladder/pinned changes ("legacy
// byte-identity" now means the pinned-free legacy caller — the pinned region
// was removed entirely in P2, D5).
func TestLegacyLayoutByteIdentity(t *testing.T) {
	const (
		model        = "claude-test"
		prompt       = "the per-turn prompt"
		systemPrompt = "system instructions"
		maxTokens    = int64(4096)
	)
	fileIDs := []string{"id-cold", "id-claude", "id-hot", "id-extra"}
	stableCount := 2

	// --- reference: the historical construction, verbatim from the pinned-free
	// pre-change client.go ---
	buildReference := func(noCache bool) anthropic.BetaMessageNewParams {
		contentBlocks := make([]anthropic.BetaContentBlockParamUnion, 0, 1+len(fileIDs))
		breakpoints := map[int]bool{}
		if !noCache {
			breakpoints[stableCount-1] = true
			breakpoints[len(fileIDs)-1] = true
		}
		for i, id := range fileIDs {
			blk := anthropic.NewBetaDocumentBlock(anthropic.BetaFileDocumentSourceParam{FileID: id})
			if breakpoints[i] {
				blk.OfDocument.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
			}
			contentBlocks = append(contentBlocks, blk)
		}
		contentBlocks = append(contentBlocks, anthropic.NewBetaTextBlock(prompt))
		params := anthropic.BetaMessageNewParams{
			Model:     anthropic.Model(model),
			MaxTokens: maxTokens,
			Messages: []anthropic.BetaMessageParam{
				anthropic.NewBetaUserMessage(contentBlocks...),
			},
			Betas: []anthropic.AnthropicBeta{anthropic.AnthropicBetaFilesAPI2025_04_14},
		}
		params.System = []anthropic.BetaTextBlockParam{{Type: "text", Text: systemPrompt}}
		return params
	}

	for _, noCache := range []bool{false, true} {
		req := GenerateRequest{
			Model:        model,
			Prompt:       prompt,
			SystemPrompt: systemPrompt,
			Regions: ContextRegions{
				Layout:      CacheLayoutLegacy,
				Files:       make([]string, len(fileIDs)),
				StableCount: stableCount,
			},
			MaxTokens: maxTokens,
			NoCache:   noCache,
			CacheTTL:  "", // legacy default: no ttl field may be serialized
		}
		got, err := json.Marshal(buildMessageParams(req, fileIDs))
		if err != nil {
			t.Fatalf("marshal new params: %v", err)
		}
		want, err := json.Marshal(buildReference(noCache))
		if err != nil {
			t.Fatalf("marshal reference params: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("noCache=%v: legacy request bytes changed.\n got: %s\nwant: %s", noCache, got, want)
		}
	}

	// Explicit pins on the expectations, so a drift in the reference builder
	// itself also fails loudly: breakpoints at {1,3}, no ttl anywhere.
	req := GenerateRequest{
		Model: model, Prompt: prompt, SystemPrompt: systemPrompt, MaxTokens: maxTokens,
		Regions: ContextRegions{Layout: CacheLayoutLegacy, Files: make([]string, len(fileIDs)), StableCount: stableCount},
	}
	plan := computeBreakpoints(req.Regions, true, false, false)
	if !equalInts(indices(plan.Docs), []int{1, 3}) {
		t.Errorf("legacy breakpoint indices = %v, want [1 3]", indices(plan.Docs))
	}
	if plan.System || plan.History {
		t.Errorf("legacy layout must not mark system/history breakpoints")
	}
	raw, err := json.Marshal(buildMessageParams(req, fileIDs))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if strings.Contains(string(raw), `"ttl"`) {
		t.Errorf("legacy request with empty CacheTTL serialized a ttl field: %s", raw)
	}
	if got, want := strings.Count(string(raw), `"cache_control"`), 2; got != want {
		t.Errorf("legacy cache_control count = %d, want %d", got, want)
	}
}

// TestLegacyAssemblyByteIdentity pins the legacy file ORDER end to end: the
// assembled order for a representative legacy RequestOptions must be exactly
// cold, CLAUDE.md, hot, context files — what the pinned-free code uploads.
func TestLegacyAssemblyByteIdentity(t *testing.T) {
	fx := newLegacyFixture(t)
	regions := assembleContextRegions(RequestOptions{
		ContextFiles: fx.contexts,
	}, fx.workDir, fx.hot, fx.cold)

	want := ContextRegions{
		Layout:      CacheLayoutLegacy,
		Files:       []string{fx.cold, fx.claudeMD, fx.hot, fx.contexts[0]},
		StableCount: 2,
	}
	if !equalRegions(regions, want) {
		t.Errorf("regions = %+v, want %+v", regions, want)
	}
}

func equalRegions(a, b ContextRegions) bool {
	if a.Layout != b.Layout || a.StableCount != b.StableCount || a.LayerCount != b.LayerCount {
		return false
	}
	if len(a.Files) != len(b.Files) {
		return false
	}
	for i := range a.Files {
		if a.Files[i] != b.Files[i] {
			return false
		}
	}
	return true
}
