package anthropic

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	corelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/grove-anthropic/pkg/config"
	grovecontext "github.com/grovetools/grove-anthropic/pkg/context"
	"github.com/grovetools/grove-anthropic/pkg/logging"
	"github.com/grovetools/grove-anthropic/pkg/models"
)

// DefaultModel is the default Claude model to use if none is specified.
// Exported from models package for centralized management.
var DefaultModel = models.DefaultModel

// ulog is the unified logger for this package
var ulog = corelogging.NewUnifiedLogger("grove-anthropic")

// Client wraps the official Anthropic client.
type Client struct {
	client anthropic.Client
}

// NewClient creates a new Anthropic client wrapper.
func NewClient(apiKeyOverride string) (*Client, error) {
	var apiKey string
	var err error

	if apiKeyOverride != "" {
		apiKey = apiKeyOverride
	} else {
		apiKey, err = config.ResolveAPIKey()
		if err != nil {
			return nil, err
		}
	}

	sdkClient := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Client{client: sdkClient}, nil
}

// GenerateContent sends a request to the Anthropic API with context files.
// stableCount is the number of leading contextFiles that form the stable,
// cacheable prefix; an Anthropic cache_control breakpoint is placed on the last
// of them unless noCache is set.
func (c *Client) GenerateContent(ctx context.Context, model, prompt, systemPrompt string, contextFiles []string, maxTokens int64, stableCount int, noCache bool) (string, *anthropic.BetaUsage, error) {
	startTime := time.Now()
	requestID := os.Getenv("GROVE_REQUEST_ID")
	contextInfo := grovecontext.GetContextInfo("")
	caller := grovecontext.GetCaller()

	// Execute the actual API call
	responseText, usage, err := c.generateContentInternal(ctx, model, prompt, systemPrompt, contextFiles, maxTokens, stableCount, noCache)

	// Log the request (success or failure)
	logEntry := logging.QueryLog{
		Timestamp:    startTime,
		RequestID:    requestID,
		Model:        model,
		ResponseTime: time.Since(startTime).Seconds(),
		Success:      err == nil,
		WorkingDir:   contextInfo.WorkingDir,
		GitRepo:      contextInfo.GitRepo,
		GitBranch:    contextInfo.GitBranch,
		GitCommit:    contextInfo.GitCommit,
		Caller:       caller,
	}

	if usage != nil {
		logEntry.InputTokens = usage.InputTokens
		logEntry.OutputTokens = usage.OutputTokens
		logEntry.CacheCreationTokens = usage.CacheCreationInputTokens
		logEntry.CacheReadTokens = usage.CacheReadInputTokens
		if totalInput := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens; totalInput > 0 {
			logEntry.CacheHitRate = float64(usage.CacheReadInputTokens) / float64(totalInput) * 100
		}
		logEntry.EstimatedCost = logging.EstimateCostWithCache(model, usage.InputTokens, usage.OutputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
	}

	if err != nil {
		logEntry.Error = err.Error()
	}

	if logErr := logging.GetLogger().Log(logEntry); logErr != nil {
		ulog.Warn("Failed to write to query log").Err(logErr).Log(ctx)
	}

	return responseText, usage, err
}

// generateContentInternal contains the core API call logic.
// Uses streaming internally to support longer requests (>10 min) with high max_tokens.
func (c *Client) generateContentInternal(ctx context.Context, model, prompt, systemPrompt string, contextFiles []string, maxTokens int64, stableCount int, noCache bool) (string, *anthropic.BetaUsage, error) {
	contentBlocks := make([]anthropic.BetaContentBlockParamUnion, 0, 1+len(contextFiles))

	// Upload context files and add them as document blocks FIRST, so that the
	// stable context forms a cacheable prefix. Files are ordered stable-first by
	// the caller (see request.go). Up to two cache_control breakpoints (the API
	// allows four): one on the last stable document (index stableCount-1), and
	// one on the last document overall. The second makes caching work even when
	// no stable/cold context exists — all documents precede the per-turn prompt
	// text, so they form a stable prefix across turns of a chat regardless of
	// how the rules file splits hot/cold. When a volatile document changes, the
	// stable breakpoint still yields a partial cache hit.
	for i, filePath := range contextFiles {
		metadata, err := uploadFile(ctx, &c.client, filePath)
		if err != nil {
			return "", nil, fmt.Errorf("uploading context file %s: %w", filePath, err)
		}

		blk := anthropic.NewBetaDocumentBlock(anthropic.BetaFileDocumentSourceParam{
			FileID: metadata.ID,
		})
		isLastStable := stableCount > 0 && i == stableCount-1
		isLastDoc := i == len(contextFiles)-1
		if !noCache && (isLastStable || isLastDoc) {
			blk.OfDocument.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
		}
		contentBlocks = append(contentBlocks, blk)
	}

	// Add the text prompt LAST so it stays outside the cacheable prefix.
	contentBlocks = append(contentBlocks, anthropic.NewBetaTextBlock(prompt))

	// Construct the message parameters
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(contentBlocks...),
		},
		Betas: []anthropic.AnthropicBeta{anthropic.AnthropicBetaFilesAPI2025_04_14},
	}

	// Add system prompt if provided
	if systemPrompt != "" {
		params.System = []anthropic.BetaTextBlockParam{{
			Type: "text",
			Text: systemPrompt,
		}}
	}

	// Use streaming API to support longer requests without timeout
	stream := c.client.Beta.Messages.NewStreaming(ctx, params)

	var fullText strings.Builder
	message := anthropic.BetaMessage{}

	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return "", nil, fmt.Errorf("accumulating stream event: %w", err)
		}

		// Extract text from deltas
		switch eventVariant := event.AsAny().(type) {
		case anthropic.BetaRawContentBlockDeltaEvent:
			switch deltaVariant := eventVariant.Delta.AsAny().(type) {
			case anthropic.BetaTextDelta:
				fullText.WriteString(deltaVariant.Text)
			}
		}
	}

	if stream.Err() != nil {
		return "", nil, fmt.Errorf("stream error: %w", stream.Err())
	}

	responseText := fullText.String()
	if responseText == "" {
		return "", &message.Usage, fmt.Errorf("no text content in response")
	}

	return responseText, &message.Usage, nil
}
