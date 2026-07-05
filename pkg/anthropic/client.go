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

// GenerateRequest carries everything GenerateContent needs to build one
// request: the prompt texts, the assembled document regions (see
// assembleContextRegions in request.go), the optional byte-stable dialogue
// history prefix, and the cache configuration.
type GenerateRequest struct {
	Model         string
	Prompt        string
	SystemPrompt  string
	Regions       ContextRegions
	HistoryPrefix string
	MaxTokens     int64
	NoCache       bool
	// CacheTTL is the TTL applied to every breakpoint in the request — one
	// TTL for all (spec 19 D2). "", CacheTTL5m, or CacheTTL1h; empty leaves
	// the SDK param's TTL field unset so it is not serialized (omitzero),
	// byte-identical to the pre-TTL behavior (the API defaults to 5m).
	CacheTTL string
}

// breakpointPlan describes where a request's cache_control breakpoints go.
// The API allows at most 4 per request, counting a system-prompt breakpoint;
// both layouts stay within budget: legacy places up to 2 document breakpoints
// (System/History are never set), ladder places up to 3 total (system + last
// layer document + history block), keeping one spare.
type breakpointPlan struct {
	System  bool         // breakpoint on the system prompt block (ladder only)
	Docs    map[int]bool // breakpoints on document indices into Regions.Files
	History bool         // breakpoint on the HistoryPrefix text block (ladder only)
}

// count returns the total number of breakpoints the plan places.
func (p breakpointPlan) count() int {
	n := len(p.Docs)
	if p.System {
		n++
	}
	if p.History {
		n++
	}
	return n
}

// computeBreakpoints returns the breakpoint plan for a request. hasSystem and
// hasHistory report whether the request carries a non-empty system prompt /
// HistoryPrefix block. noCache ⇒ zero breakpoints under both layouts.
//
// Ladder (spec 19 D1): one save point per lifetime boundary — the system
// prompt (BP1), the last LayerFiles document (BP2; ContextFiles after it get
// no breakpoint of their own), and the HistoryPrefix text block (BP3). The
// Prompt block is never cached. Max 3 of the API's 4, one spare.
//
// Legacy: exactly the historical cacheBreakpointIndices document set; the
// system prompt and history block never carry breakpoints, preserving
// byte-identity for existing callers.
func computeBreakpoints(regions ContextRegions, hasSystem, hasHistory, noCache bool) breakpointPlan {
	plan := breakpointPlan{Docs: make(map[int]bool)}
	if noCache {
		return plan
	}
	if regions.Layout == CacheLayoutLadder {
		plan.System = hasSystem
		if regions.LayerCount > 0 {
			plan.Docs[regions.LayerCount-1] = true
		}
		plan.History = hasHistory
		return plan
	}
	plan.Docs = cacheBreakpointIndices(len(regions.Files), regions.StableCount, noCache)
	return plan
}

// cacheControlParam builds the ephemeral cache_control param for one
// breakpoint, threading the request-wide TTL (spec 19 D2). Empty ttl leaves
// the TTL field unset — `omitzero` keeps it out of the serialized request,
// byte-identical to the pre-TTL behavior.
func cacheControlParam(ttl string) anthropic.BetaCacheControlEphemeralParam {
	cc := anthropic.NewBetaCacheControlEphemeralParam()
	switch ttl {
	case CacheTTL5m:
		cc.TTL = anthropic.BetaCacheControlEphemeralTTLTTL5m
	case CacheTTL1h:
		cc.TTL = anthropic.BetaCacheControlEphemeralTTLTTL1h
	}
	return cc
}

// cacheBreakpointIndices returns the set of contextFiles indices that should
// carry an Anthropic cache_control breakpoint, given the stable/volatile
// partition of the LEGACY layout. contextFiles are ordered
// [stable…][volatile…]. Up to two breakpoints are placed (the API allows
// four): a fixed one on the last stable document (stableCount-1) and a moving
// one on the last document overall (total-1). When noCache is set no
// breakpoints are placed, preserving NoCache semantics for free. This is
// exactly the historical pinned-free two-breakpoint set {stableCount-1,
// total-1} — the pinned region and its third breakpoint were removed in spec
// 19 P2 (D5), so legacy callers (who never pinned) stay byte-identical.
func cacheBreakpointIndices(total, stableCount int, noCache bool) map[int]bool {
	bps := make(map[int]bool)
	if noCache || total == 0 {
		return bps
	}
	if stableCount > 0 {
		bps[stableCount-1] = true
	}
	bps[total-1] = true
	return bps
}

// GenerateContent sends a request to the Anthropic API with context files.
// The request's document payload, region partition (which drives breakpoint
// placement — see computeBreakpoints), layout, and cache TTL all travel in
// req; RunWithUsage assembles them via assembleContextRegions.
func (c *Client) GenerateContent(ctx context.Context, req GenerateRequest) (string, *anthropic.BetaUsage, error) {
	startTime := time.Now()
	requestID := os.Getenv("GROVE_REQUEST_ID")
	contextInfo := grovecontext.GetContextInfo("")
	caller := grovecontext.GetCaller()

	// Execute the actual API call
	responseText, usage, err := c.generateContentInternal(ctx, req)

	// Log the request (success or failure)
	logEntry := logging.QueryLog{
		Timestamp:    startTime,
		RequestID:    requestID,
		Model:        req.Model,
		ResponseTime: time.Since(startTime).Seconds(),
		Success:      err == nil,
		WorkingDir:   contextInfo.WorkingDir,
		GitRepo:      contextInfo.GitRepo,
		GitBranch:    contextInfo.GitBranch,
		GitCommit:    contextInfo.GitCommit,
		Caller:       caller,
	}

	if usage != nil {
		write5m, write1h := splitCacheWrites(usage, req.CacheTTL)
		logEntry.InputTokens = usage.InputTokens
		logEntry.OutputTokens = usage.OutputTokens
		logEntry.CacheCreationTokens = usage.CacheCreationInputTokens
		logEntry.CacheWrite5mTokens = write5m
		logEntry.CacheWrite1hTokens = write1h
		logEntry.CacheReadTokens = usage.CacheReadInputTokens
		if totalInput := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens; totalInput > 0 {
			logEntry.CacheHitRate = float64(usage.CacheReadInputTokens) / float64(totalInput) * 100
		}
		logEntry.EstimatedCost = logging.EstimateCostWithCacheSplit(req.Model, usage.InputTokens, usage.OutputTokens, write5m, write1h, usage.CacheReadInputTokens)
	}

	if err != nil {
		logEntry.Error = err.Error()
	}

	if logErr := logging.GetLogger().Log(logEntry); logErr != nil {
		ulog.Warn("Failed to write to query log").Err(logErr).Log(ctx)
	}

	return responseText, usage, err
}

// buildMessageParams assembles the full message parameters for a request from
// the already-uploaded Files-API file IDs (parallel to req.Regions.Files). It
// is split from generateContentInternal so tests can assert on block order,
// breakpoint placement, and TTL threading without touching the network.
//
// Block order: document blocks FIRST (in region order, so the stable/layer
// prefix is cacheable), then the byte-stable HistoryPrefix text block when
// present (spec 19 D7), then the volatile Prompt text block LAST so it stays
// outside every cacheable prefix. Breakpoint placement per layout is
// documented on computeBreakpoints; under legacy the last-doc breakpoint
// makes caching work even when no stable/cold context exists — all documents
// precede the per-turn prompt text, so they form a stable prefix across turns
// of a chat regardless of how the rules file splits hot/cold. When a volatile
// document changes, the stable/pinned breakpoints still yield a partial cache
// hit.
func buildMessageParams(req GenerateRequest, fileIDs []string) anthropic.BetaMessageNewParams {
	plan := computeBreakpoints(req.Regions, req.SystemPrompt != "", req.HistoryPrefix != "", req.NoCache)

	contentBlocks := make([]anthropic.BetaContentBlockParamUnion, 0, 2+len(fileIDs))
	for i, fileID := range fileIDs {
		blk := anthropic.NewBetaDocumentBlock(anthropic.BetaFileDocumentSourceParam{
			FileID: fileID,
		})
		if plan.Docs[i] {
			blk.OfDocument.CacheControl = cacheControlParam(req.CacheTTL)
		}
		contentBlocks = append(contentBlocks, blk)
	}

	// Byte-stable dialogue history rides in its OWN text block so it can hold
	// a cache breakpoint (ladder BP3); only the current turn stays volatile.
	if req.HistoryPrefix != "" {
		blk := anthropic.NewBetaTextBlock(req.HistoryPrefix)
		if plan.History {
			blk.OfText.CacheControl = cacheControlParam(req.CacheTTL)
		}
		contentBlocks = append(contentBlocks, blk)
	}

	// Add the text prompt LAST so it stays outside the cacheable prefix.
	contentBlocks = append(contentBlocks, anthropic.NewBetaTextBlock(req.Prompt))

	// Construct the message parameters
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: req.MaxTokens,
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(contentBlocks...),
		},
		Betas: []anthropic.AnthropicBeta{anthropic.AnthropicBetaFilesAPI2025_04_14},
	}

	// Add system prompt if provided; under ladder it carries BP1.
	if req.SystemPrompt != "" {
		sys := anthropic.BetaTextBlockParam{
			Type: "text",
			Text: req.SystemPrompt,
		}
		if plan.System {
			sys.CacheControl = cacheControlParam(req.CacheTTL)
		}
		params.System = []anthropic.BetaTextBlockParam{sys}
	}

	return params
}

// generateContentInternal contains the core API call logic.
// Uses streaming internally to support longer requests (>10 min) with high max_tokens.
func (c *Client) generateContentInternal(ctx context.Context, req GenerateRequest) (string, *anthropic.BetaUsage, error) {
	// Upload context files first; document blocks reference the resulting
	// Files-API IDs in region order (see buildMessageParams for the layout).
	fileIDs := make([]string, 0, len(req.Regions.Files))
	for _, filePath := range req.Regions.Files {
		metadata, err := uploadFile(ctx, &c.client, filePath)
		if err != nil {
			return "", nil, fmt.Errorf("uploading context file %s: %w", filePath, err)
		}
		fileIDs = append(fileIDs, metadata.ID)
	}

	params := buildMessageParams(req, fileIDs)

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
