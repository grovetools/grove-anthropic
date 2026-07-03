package anthropic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/tui/theme"
	grovecontext "github.com/grovetools/cx/pkg/context"
	"github.com/grovetools/grove-anthropic/pkg/logging"
	"github.com/grovetools/grove-anthropic/pkg/pretty"
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
	// PinnedFiles are files placed in a stable, cacheable region *after* the
	// cold/CLAUDE.md stable half and *before* the volatile hot half, each in the
	// caller's declared order (do NOT sort). They get their own fixed
	// cache_control breakpoint (see client.go cacheBreakpointIndices), so context
	// added mid-chat preserves the already-cached stable prefix on the turn it is
	// introduced and caches durably on every subsequent turn. grove-flow sets
	// this from a chat job's `pinned_context` frontmatter. Empty ⇒ requests are
	// byte-identical to the pre-pinned two-breakpoint behavior.
	PinnedFiles []string
	// HotContextFile / ColdContextFile pin the generated/cached context to
	// explicit absolute paths instead of resolving them from WorkDir. grove-
	// flow sets these to per-job paths (under <plan>/.artifacts/<job-id>/) so
	// that concurrently dispatched jobs in one plan upload their OWN context
	// rather than racing on the shared plan-scoped files. When either is set,
	// rules-based regeneration is skipped. Empty values fall back to WorkDir
	// resolution for direct CLI callers.
	HotContextFile  string
	ColdContextFile string
	// For logging
	Caller   string
	JobID    string
	PlanName string
}

// isEmptyColdContext reports whether path is a cx-generated cached-context
// file with zero cold files (cx emits `<cold-context files="0">` when the
// rules have no `---` cold section).
func isEmptyColdContext(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return strings.Contains(string(buf[:n]), `<cold-context files="0"`)
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
	CacheReadTokens     int64
	EstimatedCostUSD    float64
	KnownPricing        bool
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

	ctxMgr := grovecontext.NewManager(workDir)

	rulesPath := ctxMgr.ResolveRulesPath()

	hotContextFile := ctxMgr.ResolveContextPath()
	coldContextFile := ctxMgr.ResolveCachedContextPath()

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

	// Collect context files, deduplicating by absolute path. Files are split
	// into a STABLE half (cold/cached context + CLAUDE.md), a PINNED region
	// (caller-supplied stable-appended files), and a VOLATILE half (hot context +
	// caller-supplied --context files). They are emitted stable→pinned→volatile
	// so the stable+pinned prefix is cacheable; Anthropic cache_control
	// breakpoints are placed on the last stable, last pinned, and last document
	// (see client.go). Dedupe precedence is stable > pinned > volatile: a path
	// present in more than one channel stays in the earliest (most stable) one,
	// which is the safe direction (it was already before the later breakpoint).
	seen := make(map[string]bool)
	var stableFiles, pinnedFiles, volatileFiles []string
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

	// Stable half: cold/cached context, then CLAUDE.md. cx always writes a
	// cached-context file, even when the rules have no cold section — skip the
	// empty stub so we don't upload it or waste a cache breakpoint on a
	// document far below the cacheable minimum.
	if _, err := os.Stat(coldContextFile); err == nil && !isEmptyColdContext(coldContextFile) {
		addFile(&stableFiles, coldContextFile)
	}
	claudePath := filepath.Join(workDir, "CLAUDE.md")
	if _, err := os.Stat(claudePath); err == nil {
		addFile(&stableFiles, claudePath)
		r.logger.Info(fmt.Sprintf("Including CLAUDE.md: %s", claudePath))
	}

	// Pinned region: caller-supplied stable-appended files, in declared order
	// (do NOT sort — position stability across turns is what keeps them cached).
	for _, f := range options.PinnedFiles {
		addFile(&pinnedFiles, f)
	}

	// Volatile half: hot context, then caller-supplied context files.
	if _, err := os.Stat(hotContextFile); err == nil {
		addFile(&volatileFiles, hotContextFile)
	}
	for _, f := range options.ContextFiles {
		addFile(&volatileFiles, f)
	}

	stableCount := len(stableFiles)
	pinnedCount := len(pinnedFiles)
	allContextFiles := append(append(append([]string{}, stableFiles...), pinnedFiles...), volatileFiles...)

	if len(allContextFiles) > 0 {
		r.logger.FilesIncludedCtx(ctx, allContextFiles)
	}

	anthropicClient, err := NewClient(options.APIKey)
	if err != nil {
		return "", nil, fmt.Errorf("creating Anthropic client: %w", err)
	}

	r.logger.ModelCtx(ctx, options.Model)

	// Make the API request
	response, usage, err := anthropicClient.GenerateContent(
		ctx,
		options.Model,
		options.Prompt,
		options.SystemPrompt,
		allContextFiles,
		options.MaxTokens,
		stableCount,
		pinnedCount,
		options.NoCache,
	)

	// Calculate response time and cost for display
	responseTime := time.Since(startTime)
	var inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64
	var estimatedCost float64
	var knownPricing bool
	var usageResult *UsageResult
	if usage != nil {
		inputTokens = usage.InputTokens
		outputTokens = usage.OutputTokens
		cacheCreationTokens = usage.CacheCreationInputTokens
		cacheReadTokens = usage.CacheReadInputTokens
		estimatedCost, knownPricing = logging.EstimateCostWithCacheOK(options.Model, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens)
		usageResult = &UsageResult{
			Model:               options.Model,
			InputTokens:         inputTokens,
			OutputTokens:        outputTokens,
			CacheCreationTokens: cacheCreationTokens,
			CacheReadTokens:     cacheReadTokens,
			EstimatedCostUSD:    estimatedCost,
			KnownPricing:        knownPricing,
		}
	}

	if err != nil {
		// usageResult is returned (may be non-nil on the error-with-usage path)
		// so callers can account for it if they choose; grove-flow does not.
		return "", usageResult, fmt.Errorf("Anthropic API request failed: %w", err)
	}

	// Display token usage
	if usage != nil {
		r.logger.TokenUsageCtx(ctx, int(inputTokens), int(outputTokens), int(cacheCreationTokens), int(cacheReadTokens), responseTime, estimatedCost)
	}

	return response, usageResult, nil
}
