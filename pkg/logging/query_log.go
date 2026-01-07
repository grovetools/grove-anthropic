package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
func EstimateCost(model string, inputTokens, outputTokens int64) float64 {
	var inputPrice, outputPrice float64 // per million tokens

	// Pricing as of Q1 2025
	switch {
	case strings.Contains(model, "opus"):
		inputPrice = 15.00
		outputPrice = 75.00
	case strings.Contains(model, "sonnet"):
		inputPrice = 3.00
		outputPrice = 15.00
	case strings.Contains(model, "haiku"):
		inputPrice = 0.25
		outputPrice = 1.25
	default: // Default to Sonnet pricing as a safe middle-ground
		inputPrice = 3.00
		outputPrice = 15.00
	}

	inputCost := (float64(inputTokens) / 1_000_000) * inputPrice
	outputCost := (float64(outputTokens) / 1_000_000) * outputPrice

	return inputCost + outputCost
}
