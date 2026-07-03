package anthropic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// Run executes a request with the given options
func (r *RequestRunner) Run(ctx context.Context, options RequestOptions) (string, error) {
	startTime := time.Now()

	if options.Prompt == "" {
		return "", fmt.Errorf("prompt cannot be empty")
	}

	workDir := options.WorkDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting current directory: %w", err)
		}
	}

	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolving work directory: %w", err)
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
			return "", fmt.Errorf("updating context from rules: %w", err)
		}
		if err := ctxMgr.GenerateContext(false); err != nil {
			return "", fmt.Errorf("generating context: %w", err)
		}
		r.logger.Blank()
	}

	// Collect context files, deduplicating by absolute path. Files are split
	// into a STABLE half (cold/cached context + CLAUDE.md) and a VOLATILE half
	// (hot context + caller-supplied --context files). The stable half is
	// emitted first so it forms a cacheable prefix; the Anthropic cache_control
	// breakpoint is placed on the last stable document (see client.go).
	seen := make(map[string]bool)
	var stableFiles, volatileFiles []string
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

	// Stable half: cold/cached context, then CLAUDE.md.
	if _, err := os.Stat(coldContextFile); err == nil {
		addFile(&stableFiles, coldContextFile)
	}
	claudePath := filepath.Join(workDir, "CLAUDE.md")
	if _, err := os.Stat(claudePath); err == nil {
		addFile(&stableFiles, claudePath)
		r.logger.Info(fmt.Sprintf("Including CLAUDE.md: %s", claudePath))
	}

	// Volatile half: hot context, then caller-supplied context files.
	if _, err := os.Stat(hotContextFile); err == nil {
		addFile(&volatileFiles, hotContextFile)
	}
	for _, f := range options.ContextFiles {
		addFile(&volatileFiles, f)
	}

	stableCount := len(stableFiles)
	allContextFiles := append(append([]string{}, stableFiles...), volatileFiles...)

	if len(allContextFiles) > 0 {
		r.logger.FilesIncludedCtx(ctx, allContextFiles)
	}

	anthropicClient, err := NewClient(options.APIKey)
	if err != nil {
		return "", fmt.Errorf("creating Anthropic client: %w", err)
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
		options.NoCache,
	)

	// Calculate response time and cost for display
	responseTime := time.Since(startTime)
	var inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64
	var estimatedCost float64
	if usage != nil {
		inputTokens = usage.InputTokens
		outputTokens = usage.OutputTokens
		cacheCreationTokens = usage.CacheCreationInputTokens
		cacheReadTokens = usage.CacheReadInputTokens
		estimatedCost = logging.EstimateCostWithCache(options.Model, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens)
	}

	if err != nil {
		return "", fmt.Errorf("Anthropic API request failed: %w", err)
	}

	// Display token usage
	if usage != nil {
		r.logger.TokenUsageCtx(ctx, int(inputTokens), int(outputTokens), int(cacheCreationTokens), int(cacheReadTokens), responseTime, estimatedCost)
	}

	return response, nil
}
