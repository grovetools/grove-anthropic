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
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheHitRate        float64   `json:"cache_hit_rate"`
	ResponseTime        float64   `json:"response_time_seconds"`
	EstimatedCost       float64   `json:"estimated_cost_usd"`
	Error               string    `json:"error,omitempty"`
	Success             bool      `json:"success"`
	WorkingDir          string    `json:"working_dir,omitempty"`
	GitRepo             string    `json:"git_repo,omitempty"`
	GitBranch           string    `json:"git_branch,omitempty"`
	GitCommit           string    `json:"git_commit,omitempty"`
	Caller              string    `json:"caller,omitempty"`
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

	// Sole remaining special case: Sonnet long-context pricing (Sonnet ≥3.7
	// bills input at 6.00 / output at 22.50 above LongContextThreshold input
	// tokens). Applied on top of the table base rate so it survives both the
	// exact and family-substring lookups for any dated Sonnet snapshot.
	resolved := models.ResolveAlias(model)
	if strings.Contains(resolved, "sonnet") && inputTokens > models.LongContextThreshold {
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
// prompt caching and reports whether the model's price was found in the model
// table (knownPricing=false means the 3/15 default was used, so the cost is a
// rough estimate). Anthropic bills cache-write tokens at 1.25x the input price
// and cache-read tokens at 0.10x the input price; regular InputTokens already
// exclude cached tokens, so the three input tiers sum without double-counting.
//
// The 1.25x/0.10x multipliers are for 5m ephemeral caching, the only cache mode
// grove's callers currently set. 1h caching carries a different (2x write)
// premium and would need revisiting here if adopted.
func EstimateCostWithCacheOK(model string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) (cost float64, knownPricing bool) {
	inputPrice, outputPrice, known := modelPrices(model, inputTokens)

	inputCost := (float64(inputTokens) / 1_000_000) * inputPrice
	cacheCreationCost := (float64(cacheCreationTokens) / 1_000_000) * inputPrice * 1.25
	cacheReadCost := (float64(cacheReadTokens) / 1_000_000) * inputPrice * 0.10
	outputCost := (float64(outputTokens) / 1_000_000) * outputPrice

	return inputCost + cacheCreationCost + cacheReadCost + outputCost, known
}
