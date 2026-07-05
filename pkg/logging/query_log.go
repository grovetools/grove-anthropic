package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/grove-anthropic/pkg/models"
)

// QueryLog represents a single API query log entry
type QueryLog struct {
	Timestamp           time.Time `json:"timestamp"`
	RequestID           string    `json:"request_id,omitempty"`
	Model               string    `json:"model"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	// CacheWrite5mTokens/CacheWrite1hTokens split CacheCreationTokens by cache
	// TTL (spec 19 D9 — ledger honesty: 1h writes bill at 2.0x the input rate,
	// 5m at 1.25x). They sum to CacheCreationTokens, which is kept for
	// compatibility with existing ledger consumers.
	CacheWrite5mTokens int64   `json:"cache_write_5m_tokens,omitempty"`
	CacheWrite1hTokens int64   `json:"cache_write_1h_tokens,omitempty"`
	CacheReadTokens    int64   `json:"cache_read_tokens"`
	CacheHitRate       float64 `json:"cache_hit_rate"`
	ResponseTime       float64 `json:"response_time_seconds"`
	EstimatedCost      float64 `json:"estimated_cost_usd"`
	Error              string  `json:"error,omitempty"`
	Success            bool    `json:"success"`
	WorkingDir         string  `json:"working_dir,omitempty"`
	GitRepo            string  `json:"git_repo,omitempty"`
	GitBranch          string  `json:"git_branch,omitempty"`
	GitCommit          string  `json:"git_commit,omitempty"`
	Caller             string  `json:"caller,omitempty"`
}

// QueryLogger handles logging API queries to a file
type QueryLogger struct {
	mu       sync.Mutex
	logFile  string
	disabled bool
}

var (
	defaultLogger *QueryLogger
	once          sync.Once
)

// GetLogger returns the singleton query logger instance
func GetLogger() *QueryLogger {
	once.Do(func() {
		logPath, err := getLogPath()
		if err != nil {
			defaultLogger = &QueryLogger{disabled: true}
			return
		}
		defaultLogger = &QueryLogger{logFile: logPath}
	})
	return defaultLogger
}

func getLogPath() (string, error) {
	stateDir := paths.StateDir()
	if stateDir == "" {
		return "", fmt.Errorf("could not determine grove state directory")
	}
	groveDir := filepath.Join(stateDir, "logs", "anthropic")
	if err := os.MkdirAll(groveDir, 0o750); err != nil { //nolint:gosec // internal log directory
		return "", err
	}
	today := time.Now().Format("2006-01-02")
	return filepath.Join(groveDir, fmt.Sprintf("query-log-%s.jsonl", today)), nil
}

// Log writes a query log entry to the log file
func (ql *QueryLogger) Log(entry QueryLog) error {
	if ql.disabled {
		return nil
	}
	ql.mu.Lock()
	defer ql.mu.Unlock()
	file, err := os.OpenFile(ql.logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // internal append-only log
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer func() { _ = file.Close() }()
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(entry); err != nil {
		return fmt.Errorf("failed to write log entry: %w", err)
	}
	return nil
}

// modelPrices resolves the per-million-token input and output prices for a
// model, plus a knownPricing flag (false when the price came from the default
// fallback rather than the model table). Pricing as of Jan 2026 - see
// https://platform.claude.com/docs/en/about-claude/pricing. inputTokens is used
// only to decide whether Sonnet long-context pricing applies.
//
// Base rates come from a single source of truth, models.GetPricingOK (exact →
// family-substring → 3/15 fallback), so this function no longer duplicates the
// per-model price literals — the old hand-maintained cascade had already
// drifted (e.g. claude-fable-5 was missing and under-billed ~3.3x as Sonnet).
// The Sonnet long-context tier is the only pricing rule that lives here, since
// it depends on the request's input-token count.
func modelPrices(model string, inputTokens int64) (inputPrice, outputPrice float64, knownPricing bool) {
	inputPrice, outputPrice, knownPricing = models.GetPricingOK(model)

	// Sole remaining special case: Sonnet 4.x long-context pricing (bills input
	// at 6.00 / output at 22.50 above LongContextThreshold input tokens).
	// Applied on top of the table base rate so it survives both the exact and
	// family-substring lookups for any dated Sonnet snapshot. Scoped to
	// "sonnet-4" so newer generations (Sonnet 5 prices flat $3/$15 across its
	// 1M window) don't silently inherit the surcharge.
	resolved := models.ResolveAlias(model)
	if strings.Contains(resolved, "sonnet-4") && inputTokens > models.LongContextThreshold {
		inputPrice = 6.00
		outputPrice = 22.50
	}

	return inputPrice, outputPrice, knownPricing
}

// EstimateCost calculates the estimated cost for a given model and token counts.
func EstimateCost(model string, inputTokens, outputTokens int64) float64 {
	return EstimateCostWithCache(model, inputTokens, outputTokens, 0, 0)
}

// EstimateCostWithCache calculates the estimated cost including Anthropic prompt
// caching. See EstimateCostWithCacheOK for the pricing details; this wrapper
// discards the known-pricing flag for callers that only need the cost.
func EstimateCostWithCache(model string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) float64 {
	cost, _ := EstimateCostWithCacheOK(model, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens)
	return cost
}

// EstimateCostWithCacheOK calculates the estimated cost including Anthropic
// prompt caching, treating ALL cache-write tokens as 5m-TTL writes (1.25x).
// Callers that know the 5m/1h split (from the API's usage.cache_creation
// detail) must use EstimateCostWithCacheSplitOK instead — a 1h write bills at
// 2.0x, so lumping it in here under-prices it (spec 19 D9).
func EstimateCostWithCacheOK(model string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) (cost float64, knownPricing bool) {
	return EstimateCostWithCacheSplitOK(model, inputTokens, outputTokens, cacheCreationTokens, 0, cacheReadTokens)
}

// EstimateCostWithCacheSplit is EstimateCostWithCacheSplitOK without the
// known-pricing flag, for callers that only need the cost.
func EstimateCostWithCacheSplit(model string, inputTokens, outputTokens, cacheWrite5m, cacheWrite1h, cacheReadTokens int64) float64 {
	cost, _ := EstimateCostWithCacheSplitOK(model, inputTokens, outputTokens, cacheWrite5m, cacheWrite1h, cacheReadTokens)
	return cost
}

// EstimateCostWithCacheSplitOK calculates the estimated cost including
// Anthropic prompt caching with the cache-write tokens split by TTL, and
// reports whether the model's price was found in the model table
// (knownPricing=false means the 3/15 default was used, so the cost is a rough
// estimate). Anthropic bills 5m-TTL cache writes at 1.25x the input price,
// 1h-TTL cache writes at 2.0x (spec 19 D2/D9), and cache reads at 0.10x;
// regular InputTokens already exclude cached tokens, so the input tiers sum
// without double-counting.
func EstimateCostWithCacheSplitOK(model string, inputTokens, outputTokens, cacheWrite5m, cacheWrite1h, cacheReadTokens int64) (cost float64, knownPricing bool) {
	inputPrice, outputPrice, known := modelPrices(model, inputTokens)

	inputCost := (float64(inputTokens) / 1_000_000) * inputPrice
	cacheWrite5mCost := (float64(cacheWrite5m) / 1_000_000) * inputPrice * 1.25
	cacheWrite1hCost := (float64(cacheWrite1h) / 1_000_000) * inputPrice * 2.0
	cacheReadCost := (float64(cacheReadTokens) / 1_000_000) * inputPrice * 0.10
	outputCost := (float64(outputTokens) / 1_000_000) * outputPrice

	return inputCost + cacheWrite5mCost + cacheWrite1hCost + cacheReadCost + outputCost, known
}
