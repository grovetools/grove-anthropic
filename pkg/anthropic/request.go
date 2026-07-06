package anthropic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/grovetools/core/tui/theme"
	grovecontext "github.com/grovetools/cx/pkg/context"
	"github.com/grovetools/grove-anthropic/pkg/logging"
	"github.com/grovetools/grove-anthropic/pkg/pretty"
)

// Cache layout names for RequestOptions.CacheLayout.
const (
	CacheLayoutLegacy = "legacy"
	CacheLayoutLadder = "ladder"
	// CacheLayoutStream is the append-only "stream" layout (spec 27). Instead
	// of segregating ALL layer documents above ALL dialogue (the ladder),
	// stream carries one ordered, heterogeneous sequence of items in Stream:
	// head-region layers, then head-region context documents, then the
	// exchanges — with any layer widened mid-chat interleaved immediately after
	// the exchange that pulled it in. Nothing is ever inserted mid-stream, so a
	// widening turn writes only the new bytes instead of re-uploading the whole
	// dialogue at the 1h-write premium (the "widen tax"). Breakpoints: system
	// (BP1) → last head-region LAYER item (BP2 — HeadAnchor) → last stored
	// layer/history item (BP3); the volatile Prompt is never cached. With every
	// layer anchored at the head and context items placed after the last layer,
	// stream is byte-identical to the ladder ("stream-at-head" equivalence),
	// which is the free migration path for existing ladder chats.
	CacheLayoutStream = "stream"
)

// Cache TTL names for RequestOptions.CacheTTL.
const (
	CacheTTL5m = "5m"
	CacheTTL1h = "1h"
)

// RequestOptions contains all the parameters for a request
type RequestOptions struct {
	Model         string
	Prompt        string
	SystemPrompt  string
	WorkDir       string
	ContextFiles  []string
	RegenerateCtx bool
	MaxTokens     int64
	NoCache       bool   // Disable Anthropic prompt caching for this request
	APIKey        string // Explicitly pass API key to avoid context issues
	// CacheLayout selects how the request payload is ordered and where the
	// Anthropic cache_control breakpoints go (spec 19 D1). Empty or
	// CacheLayoutLegacy is the historical layout — cold-context/CLAUDE.md
	// stable region, then the hot+ContextFiles volatile region —
	// byte-identical to prior releases. CacheLayoutLadder orders the payload
	// strictly by lifetime: system prompt (breakpoint) → LayerFiles documents
	// in order (breakpoint on the last) → ContextFiles documents (no own
	// breakpoint) → HistoryBlocks text blocks (breakpoint on the last) →
	// Prompt (never cached). Under ladder, WorkDir hot/cold context resolution
	// and CLAUDE.md are NOT included (D6): ladder callers pass everything
	// explicitly via LayerFiles/ContextFiles.
	CacheLayout string
	// CacheTTL selects the cache_control TTL applied to EVERY breakpoint in
	// the request — a single TTL for all, per spec 19 D2 (which also
	// sidesteps the API's 1h-before-5m ordering rule). Valid values:
	// CacheTTL5m, CacheTTL1h, or empty. Empty means the API default (5m) and
	// leaves the ttl field unserialized, keeping legacy-layout requests
	// byte-identical to prior releases.
	CacheTTL string
	// LayerFiles are the ordered, append-only layer artifacts that form the
	// ladder layout's document region (spec 19 D3). Order is preserved exactly
	// as given (never sorted): byte/position stability across turns is what
	// keeps the layer prefix cached. Ignored under the legacy layout.
	LayerFiles []string
	// LineageLayerCount is the number of LEADING layer entries (in LayerFiles
	// under ladder, or in Stream under stream) that were sourced from an
	// inherited parent lineage or dep-transcript — a contiguous prefix (K1,
	// oracle-plays/41). When 0 < LineageLayerCount < the total layer count, a
	// cache breakpoint is placed on the LAST lineage layer so sibling chats that
	// inherit the same lineage prefix cache-READ that region instead of
	// re-writing it (see computeBreakpoints). The count is a leading run only:
	// lineage integrated mid-chat on a later turn lands mid-payload and
	// deliberately gets no boundary. Callers must exclude worktree-time-varying
	// layers (e.g. the auto lineage git-delta) from this count, since those
	// bytes differ per sibling. Ignored under the legacy layout.
	LineageLayerCount int
	// HistoryBlocks is the byte-stable dialogue history (turns 1…K−1, spec 19
	// D7): ONE text block per completed prior turn, emitted in caller order
	// immediately before the volatile Prompt block. Under the ladder layout
	// the LAST history block carries a cache breakpoint (BP3).
	//
	// Why per-turn blocks instead of one concatenated history string: the
	// Anthropic cache matches stored prefixes at content-block boundaries
	// (each breakpoint's lookback checks ~20 previous BLOCK positions). A
	// single text block that grows by appending is a different block on every
	// turn, so the previous turn's save point could never match — the whole
	// history region would fall back to the last-layer breakpoint and re-write
	// at the TTL write premium every turn, which is strictly worse than not
	// caching it. With one block per turn, turn K+1 repeats turn K's blocks
	// byte-identically and appends new ones, so the previous history
	// breakpoint (one block back) hits and only the newly appended turn bytes
	// are written. Precondition: a prior turn's serialization must never
	// change retroactively — grove-flow keeps the mutating
	// status="awaiting_response"/respond_as attributes exclusively in Prompt.
	//
	// Empty elements are dropped (the API rejects empty text blocks); see
	// FilterHistoryBlocks.
	HistoryBlocks []string
	// HotContextFile / ColdContextFile pin the generated/cached context to
	// explicit absolute paths instead of resolving them from WorkDir. grove-
	// flow sets these to per-job paths (under <plan>/.artifacts/<job-id>/) so
	// that concurrently dispatched jobs in one plan upload their OWN context
	// rather than racing on the shared plan-scoped files. When either is set,
	// rules-based regeneration is skipped. Empty values fall back to WorkDir
	// resolution for direct CLI callers.
	HotContextFile  string
	ColdContextFile string
	// Stream is the ordered, heterogeneous item sequence for the stream layout
	// (spec 27), replacing the LayerFiles+ContextFiles+HistoryBlocks split.
	// Under CacheLayoutStream every document and history block travels here in
	// exact wire order, and LayerFiles/HistoryBlocks/ContextFiles/HotContextFile/
	// ColdContextFile MUST be empty (enforced in validateCacheOptions). Ignored
	// under legacy/ladder.
	Stream []StreamItem
	// For logging
	Caller   string
	JobID    string
	PlanName string
}

// StreamItem is one block of the stream layout's ordered sequence (spec 27).
// Kind is one of RequestBlockLayer / RequestBlockContext / RequestBlockHistory:
// layer and context items carry Path (a file uploaded via the Files API);
// history items carry Text (a pre-serialized <turn> block). The volatile
// per-turn prompt is NOT a StreamItem — it stays in RequestOptions.Prompt.
type StreamItem struct {
	Kind string // RequestBlockLayer | RequestBlockContext | RequestBlockHistory
	Path string // layer/context kinds: file uploaded via Files API
	Text string // history kind: pre-serialized <turn> block
}

// effectiveCacheLayout resolves the layout, defaulting empty to legacy.
func (o RequestOptions) effectiveCacheLayout() string {
	if o.CacheLayout == "" {
		return CacheLayoutLegacy
	}
	return o.CacheLayout
}

// validateCacheOptions rejects unknown CacheLayout/CacheTTL values up front so
// a typo fails loudly instead of silently degrading cache behavior.
func (o RequestOptions) validateCacheOptions() error {
	switch o.CacheLayout {
	case "", CacheLayoutLegacy, CacheLayoutLadder:
	case CacheLayoutStream:
		// Stream is the single ordered source of truth: every document/history
		// block rides in Stream, so the split fields must be empty or the wire
		// order is ambiguous.
		if len(o.LayerFiles) > 0 || len(o.HistoryBlocks) > 0 || len(o.ContextFiles) > 0 || o.HotContextFile != "" || o.ColdContextFile != "" {
			return fmt.Errorf("stream layout requires all documents in Stream; LayerFiles/HistoryBlocks/ContextFiles/HotContextFile/ColdContextFile must be empty")
		}
		for _, it := range o.Stream {
			switch it.Kind {
			case RequestBlockLayer, RequestBlockContext, RequestBlockHistory:
			default:
				return fmt.Errorf("invalid StreamItem kind %q (valid: %q, %q, %q)", it.Kind, RequestBlockLayer, RequestBlockContext, RequestBlockHistory)
			}
		}
	default:
		return fmt.Errorf("invalid CacheLayout %q (valid: %q, %q, %q, or empty for legacy)", o.CacheLayout, CacheLayoutLegacy, CacheLayoutLadder, CacheLayoutStream)
	}
	switch o.CacheTTL {
	case "", CacheTTL5m, CacheTTL1h:
	default:
		return fmt.Errorf("invalid CacheTTL %q (valid: %q, %q, or empty for the API default)", o.CacheTTL, CacheTTL5m, CacheTTL1h)
	}
	return nil
}

// ContextRegions is the assembled document payload of one request: the ordered
// files to upload plus the region partition client.go needs to place cache
// breakpoints (see computeBreakpoints). Exactly one partition applies,
// selected by Layout:
//   - legacy: Files = stable ++ volatile, described by StableCount (the
//     remainder is volatile)
//   - ladder: Files = layers ++ context, described by LayerCount (the
//     remainder is caller ContextFiles, covered by the downstream history/
//     spare breakpoints rather than one of their own)
type ContextRegions struct {
	Layout      string
	Files       []string
	StableCount int // legacy only: cold-context + CLAUDE.md prefix length
	LayerCount  int // ladder only: layer-document prefix length
	// LineageCount is the length of the leading lineage-sourced layer prefix
	// (K1, oracle-plays/41), after dedupe. Under ladder it indexes Files (the
	// last lineage layer is Files[LineageCount-1]); under stream it indexes
	// Items (the last lineage layer is Items[LineageCount-1]). computeBreakpoints
	// places the lineage-boundary breakpoint there when 0 < LineageCount and the
	// boundary is a STRICT prefix of the layers (so it does not coincide with the
	// last-layer/HeadAnchor breakpoint). 0 ⇒ no lineage prefix ⇒ no boundary
	// breakpoint, keeping non-lineage chats byte-identical.
	LineageCount int
	// Items and HeadAnchor describe the stream layout (spec 27), valid only
	// when Layout == CacheLayoutStream. Items is the ordered heterogeneous
	// block sequence (layer/context documents + history text blocks);
	// HeadAnchor is the index of the last head-region LAYER item (BP2's
	// position), or -1 when there are no layer items.
	Items      []StreamItem
	HeadAnchor int
}

// assembleContextRegions builds the ordered document file list and region
// metadata for a request from RequestOptions plus the resolved context paths.
// It is pure apart from os.Stat existence checks on the resolved paths, so it
// is directly testable.
//
// Legacy layout (the default): files are split into a STABLE half
// (cold/cached context + CLAUDE.md) and a VOLATILE half (hot context +
// caller-supplied --context files), emitted stable→volatile so the stable
// prefix is cacheable. Dedupe precedence is stable > volatile: a path present
// in both channels stays in the earlier (stable) one, which is the safe
// direction (it was already before the later breakpoint). This must remain
// byte-identical to the historical pinned-free behavior — see
// TestLegacyLayoutByteIdentity.
//
// Ladder layout (spec 19 D1/D6): LayerFiles in caller order form the layer
// region; ContextFiles follow. workDir/hot/cold are not consulted (no
// CLAUDE.md, no cx hot/cold artifacts). Dedupe precedence is layers > context
// for the same reason as above.
func assembleContextRegions(options RequestOptions, workDir, hotContextFile, coldContextFile string) ContextRegions {
	seen := make(map[string]bool)
	addFile := func(dst *[]string, path string) {
		absPath, err := filepath.Abs(path)
		if err != nil {
			absPath = path
		}
		if !seen[absPath] {
			seen[absPath] = true
			*dst = append(*dst, path)
		}
	}

	if options.effectiveCacheLayout() == CacheLayoutStream {
		// Stream: preserve caller order exactly (position stability is the
		// cache guarantee), deduping only Path-bearing items by abs path —
		// keep the first occurrence, drop later dupes. History items always
		// pass through untouched.
		items := make([]StreamItem, 0, len(options.Stream))
		lineageCount := 0
		for i, it := range options.Stream {
			if it.Path != "" {
				absPath, err := filepath.Abs(it.Path)
				if err != nil {
					absPath = it.Path
				}
				if seen[absPath] {
					continue
				}
				seen[absPath] = true
			}
			items = append(items, it)
			// A surviving item drawn from the leading lineage run (K1) extends the
			// lineage prefix; deduped-away lineage items are correctly skipped so
			// LineageCount always indexes a real, present item.
			if i < options.LineageLayerCount {
				lineageCount++
			}
		}
		// HeadAnchor = index of the last layer item preceding the first history
		// item (or the last layer item overall when there is no history). This
		// is where BP2 sits; context items after it never move it. -1 ⇒ no
		// layers ⇒ no head anchor.
		firstHistory := -1
		for i, it := range items {
			if it.Kind == RequestBlockHistory {
				firstHistory = i
				break
			}
		}
		headAnchor := -1
		for i, it := range items {
			if firstHistory >= 0 && i >= firstHistory {
				break
			}
			if it.Kind == RequestBlockLayer {
				headAnchor = i
			}
		}
		return ContextRegions{
			Layout:       CacheLayoutStream,
			Items:        items,
			HeadAnchor:   headAnchor,
			LineageCount: lineageCount,
		}
	}

	if options.effectiveCacheLayout() == CacheLayoutLadder {
		var layerFiles, contextFiles []string
		lineageCount := 0
		for i, f := range options.LayerFiles {
			before := len(layerFiles)
			addFile(&layerFiles, f)
			// A surviving layer drawn from the leading lineage run (indices <
			// LineageLayerCount) extends the lineage prefix (K1). Deduped-away
			// lineage paths are not counted, so LineageCount stays clamped to the
			// deduped layer list and the boundary always lands on a present doc.
			if len(layerFiles) > before && i < options.LineageLayerCount {
				lineageCount++
			}
		}
		for _, f := range options.ContextFiles {
			addFile(&contextFiles, f)
		}
		return ContextRegions{
			Layout:       CacheLayoutLadder,
			Files:        append(append([]string{}, layerFiles...), contextFiles...),
			LayerCount:   len(layerFiles),
			LineageCount: lineageCount,
		}
	}

	var stableFiles, volatileFiles []string

	// Stable half: cold/cached context, then CLAUDE.md. cx always writes a
	// cached-context file, even when the rules have no cold section — skip the
	// empty stub so we don't upload it or waste a cache breakpoint on a
	// document far below the cacheable minimum.
	if coldContextFile != "" {
		if _, err := os.Stat(coldContextFile); err == nil && !IsEmptyColdContext(coldContextFile) {
			addFile(&stableFiles, coldContextFile)
		}
	}
	claudePath := filepath.Join(workDir, "CLAUDE.md")
	if _, err := os.Stat(claudePath); err == nil {
		addFile(&stableFiles, claudePath)
	}

	// Volatile half: hot context, then caller-supplied context files.
	if hotContextFile != "" {
		if _, err := os.Stat(hotContextFile); err == nil {
			addFile(&volatileFiles, hotContextFile)
		}
	}
	for _, f := range options.ContextFiles {
		addFile(&volatileFiles, f)
	}

	return ContextRegions{
		Layout:      CacheLayoutLegacy,
		Files:       append(append([]string{}, stableFiles...), volatileFiles...),
		StableCount: len(stableFiles),
	}
}

// FilterHistoryBlocks drops empty elements from a HistoryBlocks slice while
// preserving order. The API rejects empty text blocks, and the request plan
// (DescribeRequest) must agree block-for-block with what actually uploads, so
// both paths — and callers describing requests (grove-flow's manifest) —
// share this normalization.
func FilterHistoryBlocks(blocks []string) []string {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b != "" {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// IsEmptyColdContext reports whether path is a cx-generated cached-context
// file with zero cold files (cx emits `<cold-context files="0">` when the
// rules have no `---` cold section). Exported so ladder callers (grove-flow)
// can apply the same skip-the-empty-stub rule when assembling LayerFiles.
func IsEmptyColdContext(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return strings.Contains(string(buf[:n]), `<cold-context files="0"`)
}

// Block kinds reported by DescribeRequest, in the vocabulary of the spec-19
// per-turn request manifest (D9).
const (
	RequestBlockSystem  = "system"  // API system param
	RequestBlockLayer   = "layer"   // ladder LayerFiles document
	RequestBlockContext = "context" // other document (ContextFiles / legacy docs)
	RequestBlockHistory = "history" // byte-stable dialogue-history text block
	RequestBlockTurn    = "turn"    // volatile per-turn prompt text block
)

// RequestPlanEntry describes one block of an assembled request, in emission
// order: what kind of block it is, which file backs it (document blocks only),
// and whether it carries a cache_control breakpoint (and at which TTL).
type RequestPlanEntry struct {
	Kind       string
	Path       string // document blocks: the file that will be uploaded; empty for text blocks
	Breakpoint bool
	TTL        string // the breakpoint's cache TTL ("" = API default 5m); empty when Breakpoint is false
}

// DescribeRequest returns the ordered block plan a request built from options
// would emit — the exact document/text block sequence of buildMessageParams
// with per-block breakpoint and TTL placement — without any network side
// effects. It exists so callers (grove-flow's per-turn request manifest, spec
// 19 D9) can record what will be uploaded and where the cache save points sit
// without duplicating region-assembly/breakpoint logic outside this package.
//
// It shares assembleContextRegions and computeBreakpoints with the live
// request path. Under the legacy layout hot/cold context paths resolve from
// WorkDir (with HotContextFile/ColdContextFile overrides) exactly as
// RunWithUsage resolves them, but rules-based regeneration is NOT run — the
// description reflects the files as they exist on disk right now.
func DescribeRequest(options RequestOptions) ([]RequestPlanEntry, error) {
	if err := options.validateCacheOptions(); err != nil {
		return nil, err
	}

	workDir := options.WorkDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting current directory: %w", err)
		}
	}

	var hotContextFile, coldContextFile string
	if options.effectiveCacheLayout() == CacheLayoutLegacy {
		ctxMgr := grovecontext.NewManager(workDir)
		hotContextFile = ctxMgr.ResolveContextPath()
		coldContextFile = ctxMgr.ResolveCachedContextPath()
		if options.HotContextFile != "" {
			hotContextFile = options.HotContextFile
		}
		if options.ColdContextFile != "" {
			coldContextFile = options.ColdContextFile
		}
	}

	regions := assembleContextRegions(options, workDir, hotContextFile, coldContextFile)
	history := FilterHistoryBlocks(options.HistoryBlocks)
	plan := computeBreakpoints(regions, options.SystemPrompt != "", len(history) > 0, options.NoCache)

	ttlFor := func(breakpoint bool) string {
		if breakpoint {
			return options.CacheTTL
		}
		return ""
	}

	// Stream layout: emit one entry per ordered item (in true wire order),
	// then the volatile turn. Breakpoints come from plan.Docs indexed by item
	// position — see computeBreakpoints' stream branch.
	if regions.Layout == CacheLayoutStream {
		entries := make([]RequestPlanEntry, 0, len(regions.Items)+2)
		if options.SystemPrompt != "" {
			entries = append(entries, RequestPlanEntry{Kind: RequestBlockSystem, Breakpoint: plan.System, TTL: ttlFor(plan.System)})
		}
		for i, it := range regions.Items {
			entries = append(entries, RequestPlanEntry{Kind: it.Kind, Path: it.Path, Breakpoint: plan.Docs[i], TTL: ttlFor(plan.Docs[i])})
		}
		entries = append(entries, RequestPlanEntry{Kind: RequestBlockTurn})
		return entries, nil
	}

	entries := make([]RequestPlanEntry, 0, len(regions.Files)+len(history)+2)
	if options.SystemPrompt != "" {
		entries = append(entries, RequestPlanEntry{Kind: RequestBlockSystem, Breakpoint: plan.System, TTL: ttlFor(plan.System)})
	}
	for i, f := range regions.Files {
		kind := RequestBlockContext
		if regions.Layout == CacheLayoutLadder && i < regions.LayerCount {
			kind = RequestBlockLayer
		}
		entries = append(entries, RequestPlanEntry{Kind: kind, Path: f, Breakpoint: plan.Docs[i], TTL: ttlFor(plan.Docs[i])})
	}
	// One entry per history block; only the LAST history block can carry the
	// history breakpoint (see HistoryBlocks on RequestOptions).
	for i := range history {
		bp := plan.History && i == len(history)-1
		entries = append(entries, RequestPlanEntry{Kind: RequestBlockHistory, Breakpoint: bp, TTL: ttlFor(bp)})
	}
	// The per-turn prompt block is always emitted last and never cached.
	entries = append(entries, RequestPlanEntry{Kind: RequestBlockTurn})
	return entries, nil
}

// RequestRunner handles the orchestration of Anthropic API requests with context management
type RequestRunner struct {
	logger *pretty.Logger
}

// NewRequestRunner creates a new RequestRunner instance
func NewRequestRunner() *RequestRunner {
	return &RequestRunner{
		logger: pretty.New(),
	}
}

// UsageResult carries the token counts and estimated cost of a single request,
// surfaced to in-process callers (e.g. grove-flow) that need to record per-job
// usage without reading back the query-log ledger. Token counts are exact from
// the API response; EstimatedCostUSD is derived from the model price table and
// KnownPricing reports whether that model was actually in the table (false ⇒
// the cost used the default fallback rate and is only a rough estimate).
type UsageResult struct {
	Model               string
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	// CacheWrite5m/CacheWrite1h split CacheCreationTokens by cache TTL (spec
	// 19 D9 — ledger honesty: 1h writes bill at 2.0x the input rate, 5m at
	// 1.25x). They come from the API's usage.cache_creation detail and always
	// sum to CacheCreationTokens: when the API omits the detail (older
	// responses), the flat total is attributed to the TTL this request asked
	// for (see splitCacheWrites).
	CacheWrite5m     int64
	CacheWrite1h     int64
	CacheReadTokens  int64
	EstimatedCostUSD float64
	KnownPricing     bool
}

// splitCacheWrites returns the 5m/1h split of a response's cache-write tokens.
// Modern responses carry the split in usage.cache_creation; when both split
// fields are zero while the flat cache_creation_input_tokens total is not, the
// flat total is attributed to requestTTL — every breakpoint in a request
// carries one TTL (spec 19 D2), and an empty TTL means the API default (5m).
func splitCacheWrites(usage *sdk.BetaUsage, requestTTL string) (write5m, write1h int64) {
	write5m = usage.CacheCreation.Ephemeral5mInputTokens
	write1h = usage.CacheCreation.Ephemeral1hInputTokens
	if write5m == 0 && write1h == 0 && usage.CacheCreationInputTokens > 0 {
		if requestTTL == CacheTTL1h {
			write1h = usage.CacheCreationInputTokens
		} else {
			write5m = usage.CacheCreationInputTokens
		}
	}
	return write5m, write1h
}

// newUsageResult converts an API usage payload into a UsageResult, splitting
// cache writes by TTL and pricing 1h writes at their 2.0x premium.
func newUsageResult(model string, usage *sdk.BetaUsage, requestTTL string) *UsageResult {
	write5m, write1h := splitCacheWrites(usage, requestTTL)
	cost, known := logging.EstimateCostWithCacheSplitOK(model, usage.InputTokens, usage.OutputTokens, write5m, write1h, usage.CacheReadInputTokens)
	return &UsageResult{
		Model:               model,
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationInputTokens,
		CacheWrite5m:        write5m,
		CacheWrite1h:        write1h,
		CacheReadTokens:     usage.CacheReadInputTokens,
		EstimatedCostUSD:    cost,
		KnownPricing:        known,
	}
}

// Run executes a request with the given options. It is a thin wrapper around
// RunWithUsage that discards the usage result, preserving the historical
// (string, error) signature for callers that don't need token accounting.
func (r *RequestRunner) Run(ctx context.Context, options RequestOptions) (string, error) {
	response, _, err := r.RunWithUsage(ctx, options)
	return response, err
}

// RunWithUsage executes a request and additionally returns the token/cost usage
// for the call. On success the *UsageResult is always non-nil. On error it is
// non-nil only when the API returned usage alongside the error (e.g. the
// "no text content in response" path) — callers may inspect it or ignore it;
// grove-flow ignores usage on error and lets the query-log ledger capture it.
func (r *RequestRunner) RunWithUsage(ctx context.Context, options RequestOptions) (string, *UsageResult, error) {
	startTime := time.Now()

	if options.Prompt == "" {
		return "", nil, fmt.Errorf("prompt cannot be empty")
	}
	if err := options.validateCacheOptions(); err != nil {
		return "", nil, err
	}

	workDir := options.WorkDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return "", nil, fmt.Errorf("getting current directory: %w", err)
		}
	}

	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", nil, fmt.Errorf("resolving work directory: %w", err)
	}
	workDir = absWorkDir
	r.logger.WorkingDirectoryCtx(ctx, workDir)

	// Hot/cold context resolution + rules-based regeneration are legacy-layout
	// concerns. Ladder callers pass every document explicitly via
	// LayerFiles/ContextFiles (spec 19 D6), so none of it applies there.
	var hotContextFile, coldContextFile string
	if options.effectiveCacheLayout() == CacheLayoutLegacy {
		ctxMgr := grovecontext.NewManager(workDir)

		rulesPath := ctxMgr.ResolveRulesPath()

		hotContextFile = ctxMgr.ResolveContextPath()
		coldContextFile = ctxMgr.ResolveCachedContextPath()

		// Job-scoped overrides from grove-flow take precedence over WorkDir
		// resolution. When set, flow has already generated the context, so we use
		// these explicit paths and skip rules-based regeneration below.
		explicitContext := options.HotContextFile != "" || options.ColdContextFile != ""
		if options.HotContextFile != "" {
			hotContextFile = options.HotContextFile
		}
		if options.ColdContextFile != "" {
			coldContextFile = options.ColdContextFile
		}

		hasRules := false
		if _, err := os.Stat(rulesPath); err == nil {
			hasRules = true
			r.logger.FoundRulesFileCtx(ctx, rulesPath)
		}

		if hasRules && options.RegenerateCtx && !explicitContext {
			r.logger.Blank()
			r.logger.Progress(theme.IconSync + " Regenerating context from rules...")
			if err := ctxMgr.UpdateFromRules(); err != nil {
				return "", nil, fmt.Errorf("updating context from rules: %w", err)
			}
			if err := ctxMgr.GenerateContext(false); err != nil {
				return "", nil, fmt.Errorf("generating context: %w", err)
			}
			r.logger.Blank()
		}
	}

	// Assemble the ordered document payload + region metadata for breakpoint
	// placement (see assembleContextRegions for the per-layout semantics).
	regions := assembleContextRegions(options, workDir, hotContextFile, coldContextFile)

	if regions.Layout == CacheLayoutLegacy {
		claudePath := filepath.Join(workDir, "CLAUDE.md")
		for _, f := range regions.Files[:regions.StableCount] {
			if f == claudePath {
				r.logger.Info(fmt.Sprintf("Including CLAUDE.md: %s", claudePath))
			}
		}
	}

	if len(regions.Files) > 0 {
		r.logger.FilesIncludedCtx(ctx, regions.Files)
	}

	anthropicClient, err := NewClient(options.APIKey)
	if err != nil {
		return "", nil, fmt.Errorf("creating Anthropic client: %w", err)
	}

	r.logger.ModelCtx(ctx, options.Model)

	// Make the API request
	response, usage, err := anthropicClient.GenerateContent(ctx, GenerateRequest{
		Model:         options.Model,
		Prompt:        options.Prompt,
		SystemPrompt:  options.SystemPrompt,
		Regions:       regions,
		HistoryBlocks: FilterHistoryBlocks(options.HistoryBlocks),
		MaxTokens:     options.MaxTokens,
		NoCache:       options.NoCache,
		CacheTTL:      options.CacheTTL,
	})

	// Calculate response time and cost for display
	responseTime := time.Since(startTime)
	var usageResult *UsageResult
	if usage != nil {
		usageResult = newUsageResult(options.Model, usage, options.CacheTTL)
	}

	if err != nil {
		// usageResult is returned (may be non-nil on the error-with-usage path)
		// so callers can account for it if they choose; grove-flow does not.
		return "", usageResult, fmt.Errorf("Anthropic API request failed: %w", err)
	}

	// Display token usage
	if usageResult != nil {
		r.logger.TokenUsageCtx(ctx, int(usageResult.InputTokens), int(usageResult.OutputTokens), int(usageResult.CacheCreationTokens), int(usageResult.CacheReadTokens), responseTime, usageResult.EstimatedCostUSD)
	}

	return response, usageResult, nil
}
