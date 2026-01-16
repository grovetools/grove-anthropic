package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/grovetools/grove-anthropic/pkg/models"
)

// QueryLog represents a single API query log entry
type QueryLog struct {
	Timestamp     time.Time `json:"timestamp"`
	RequestID     string    `json:"request_id,omitempty"`
	Model         string    `json:"model"`
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	ResponseTime  float64   `json:"response_time_seconds"`
	EstimatedCost float64   `json:"estimated_cost_usd"`
	Error         string    `json:"error,omitempty"`
	Success       bool      `json:"success"`
	WorkingDir    string    `json:"working_dir,omitempty"`
	GitRepo       string    `json:"git_repo,omitempty"`
	GitBranch     string    `json:"git_branch,omitempty"`
	GitCommit     string    `json:"git_commit,omitempty"`
	Caller        string    `json:"caller,omitempty"`
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
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	groveDir := filepath.Join(homeDir, ".grove", "anthropic-logs")
	if err := os.MkdirAll(groveDir, 0755); err != nil {
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
	file, err := os.OpenFile(ql.logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(entry); err != nil {
		return fmt.Errorf("failed to write log entry: %w", err)
	}
	return nil
}

// EstimateCost calculates the estimated cost for a given model and token counts
// Pricing as of Jan 2026 - see https://platform.claude.com/docs/en/about-claude/pricing
func EstimateCost(model string, inputTokens, outputTokens int64) float64 {
	// Resolve alias first
	model = models.ResolveAlias(model)

	var inputPrice, outputPrice float64 // per million tokens

	// Check if this is a Sonnet model eligible for long context pricing
	isSonnet := strings.Contains(model, "sonnet-4-5") || strings.Contains(model, "sonnet-4.5") ||
		strings.Contains(model, "sonnet-4") || strings.Contains(model, "sonnet")
	useLongContextPricing := isSonnet && inputTokens > models.LongContextThreshold

	switch {
	// Claude 4.5 models (current)
	case strings.Contains(model, "opus-4-5") || strings.Contains(model, "opus-4.5"):
		inputPrice = 5.00
		outputPrice = 25.00
	case strings.Contains(model, "sonnet-4-5") || strings.Contains(model, "sonnet-4.5"):
		if useLongContextPricing {
			inputPrice = 6.00
			outputPrice = 22.50
		} else {
			inputPrice = 3.00
			outputPrice = 15.00
		}
	case strings.Contains(model, "haiku-4-5") || strings.Contains(model, "haiku-4.5"):
		inputPrice = 1.00
		outputPrice = 5.00

	// Claude 4.x legacy models
	case strings.Contains(model, "opus-4-1") || strings.Contains(model, "opus-4.1"):
		inputPrice = 15.00
		outputPrice = 75.00
	case strings.Contains(model, "opus-4"):
		inputPrice = 15.00
		outputPrice = 75.00
	case strings.Contains(model, "sonnet-4") || strings.Contains(model, "sonnet-3-7") || strings.Contains(model, "sonnet-3.7"):
		if useLongContextPricing {
			inputPrice = 6.00
			outputPrice = 22.50
		} else {
			inputPrice = 3.00
			outputPrice = 15.00
		}

	// Claude 3.x legacy models
	case strings.Contains(model, "haiku-3-5") || strings.Contains(model, "haiku-3.5"):
		inputPrice = 0.80
		outputPrice = 4.00
	case strings.Contains(model, "haiku-3") || strings.Contains(model, "haiku"):
		inputPrice = 0.25
		outputPrice = 1.25
	case strings.Contains(model, "opus-3") || strings.Contains(model, "opus"):
		inputPrice = 15.00
		outputPrice = 75.00
	case strings.Contains(model, "sonnet"):
		if useLongContextPricing {
			inputPrice = 6.00
			outputPrice = 22.50
		} else {
			inputPrice = 3.00
			outputPrice = 15.00
		}

	default: // Default to Sonnet 4.5 pricing as a safe middle-ground
		inputPrice = 3.00
		outputPrice = 15.00
	}

	inputCost := (float64(inputTokens) / 1_000_000) * inputPrice
	outputCost := (float64(outputTokens) / 1_000_000) * outputPrice

	return inputCost + outputCost
}
