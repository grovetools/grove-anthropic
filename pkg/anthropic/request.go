package anthropic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/grovetools/core/pkg/workspace"
	grovecontext "github.com/grovetools/cx/pkg/context"
	"github.com/grovetools/grove-anthropic/pkg/logging"
	"github.com/grovetools/grove-anthropic/pkg/pretty"
	"github.com/grovetools/core/tui/theme"
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
	APIKey        string // Explicitly pass API key to avoid context issues
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
	node, _ := workspace.GetProjectByPath(workDir)

	rulesPath := filepath.Join(workDir, ".grove", "rules")
	if rp, err := ctxMgr.Locator().GetContextRulesFile(node); err == nil {
		rulesPath = rp
	}

	hotContextFile := filepath.Join(workDir, ".grove", "context")
	if genDir, err := ctxMgr.Locator().GetContextGeneratedDir(node); err == nil {
		hotContextFile = filepath.Join(genDir, "context")
	}

	coldContextFile := filepath.Join(workDir, ".grove", "cached-context")
	if cacheDir, err := ctxMgr.Locator().GetContextCacheDir(node); err == nil {
		coldContextFile = filepath.Join(cacheDir, "cached-context")
	}

	var allContextFiles []string
	hasRules := false
	if _, err := os.Stat(rulesPath); err == nil {
		hasRules = true
		r.logger.FoundRulesFileCtx(ctx, rulesPath)
	}

	if hasRules && options.RegenerateCtx {
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

	// Collect all context files to be uploaded, deduplicating by absolute path
	seen := make(map[string]bool)
	addFile := func(path string) {
		absPath, err := filepath.Abs(path)
		if err != nil {
			absPath = path
		}
		if !seen[absPath] {
			seen[absPath] = true
			allContextFiles = append(allContextFiles, path)
		}
	}

	if _, err := os.Stat(hotContextFile); err == nil {
		addFile(hotContextFile)
	}
	if _, err := os.Stat(coldContextFile); err == nil {
		addFile(coldContextFile)
	}
	for _, f := range options.ContextFiles {
		addFile(f)
	}

	// Also check for CLAUDE.md in the working directory
	claudePath := filepath.Join(workDir, "CLAUDE.md")
	if _, err := os.Stat(claudePath); err == nil {
		addFile(claudePath)
		r.logger.Info(fmt.Sprintf("Including CLAUDE.md: %s", claudePath))
	}

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
	)

	// Calculate response time and cost for display
	responseTime := time.Since(startTime)
	var inputTokens, outputTokens int64
	var estimatedCost float64
	if usage != nil {
		inputTokens = usage.InputTokens
		outputTokens = usage.OutputTokens
		estimatedCost = logging.EstimateCost(options.Model, inputTokens, outputTokens)
	}

	if err != nil {
		return "", fmt.Errorf("Anthropic API request failed: %w", err)
	}

	// Display token usage
	if usage != nil {
		r.logger.TokenUsageCtx(ctx, int(inputTokens), int(outputTokens), responseTime, estimatedCost)
	}

	return response, nil
}
