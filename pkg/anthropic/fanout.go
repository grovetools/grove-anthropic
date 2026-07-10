package anthropic

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	cxcontext "github.com/grovetools/cx/pkg/context"
	anthropiccontext "github.com/grovetools/grove-anthropic/pkg/context"
	"github.com/grovetools/grove-anthropic/pkg/models"
)

// Message roles for a MessageTurn in a multi-turn SharedPrefix request.
const (
	MessageRoleUser      = "user"
	MessageRoleAssistant = "assistant"
)

// MessageTurn is one completed conversation turn replayed by
// SharedPrefix.RequestWithHistory: a Role (MessageRoleUser or
// MessageRoleAssistant) and its text Content. A history slice is the prior
// dialogue in order, starting with the turn-0 user turn — replayed verbatim
// between the cached prefix and the new final user turn.
type MessageTurn struct {
	Role    string
	Content string
}

// DefaultFanoutMaxTokens is the max_tokens applied to fan-out requests when the
// caller leaves SharedPrefixOptions.MaxTokens at zero — matching the CLI's
// `request --max-tokens` default so a fan-out generation is not truncated
// shorter than a one-shot request would be.
const DefaultFanoutMaxTokens int64 = 8192

// SharedPrefixOptions configures a SharedPrefix fan-out wave.
type SharedPrefixOptions struct {
	// Model is the model ID or alias every request in the wave runs against
	// (the cache is keyed per model, so one prefix serves exactly one model).
	// Aliases are resolved to their full API ID. Required.
	Model string
	// TTL is the cache_control TTL for the prefix breakpoint: CacheTTL5m
	// (default) or CacheTTL1h. A longer TTL is worth it when the wave — or a
	// later re-run against the same prefix — spans more than five minutes.
	TTL string
	// MaxTokens caps each request's response length; 0 ⇒ DefaultFanoutMaxTokens.
	MaxTokens int64
	// APIKey overrides key resolution; empty ⇒ config.ResolveAPIKey (env then
	// grove config), the same resolution the one-shot client uses.
	APIKey string
	// Caller/JobID/PlanName are recorded in the query-log ledger for each
	// request. Caller defaults to the ambient grove caller when empty.
	Caller   string
	JobID    string
	PlanName string
}

// SharedPrefix is a byte-identical Anthropic prompt prefix — a set of context
// documents cached behind a single cache_control breakpoint at its end —
// against which N task requests can be fanned out. The first request pays the
// full cache_creation cost to write the prefix; every subsequent request reads
// it back at ~0.1x the input rate (cache_read), so a wave of M sections that
// share a large repo context pays for that context roughly once instead of M
// times.
//
// Mechanics: the prefix documents are uploaded to the Files API exactly once
// (on the first Request) and their file IDs are reused for every subsequent
// request; each request appends only its own task prompt after the breakpoint,
// so the cached prefix stays byte-identical across the wave. The layout is the
// ladder layout with every prefix document treated as a layer and the
// breakpoint on the last one (system, if provided, carries its own BP1) — the
// per-turn task prompt is the volatile block and is never cached.
//
// Serialization of the first request: Anthropic only serves a cache READ once
// the corresponding WRITE has landed, so siblings that fire before the first
// request completes would each cache-MISS and redundantly re-write the prefix.
// Request therefore designates the first caller as the writer and blocks all
// other callers until that first request returns (its write has landed) before
// they fire. Sequential callers (e.g. docgen's per-section loop) get this for
// free; concurrent callers are made safe by the same gate. A SharedPrefix is
// safe for concurrent use by multiple goroutines.
type SharedPrefix struct {
	client    *Client
	system    string
	model     string // resolved full API ID
	ttl       string
	maxTokens int64
	caller    string
	jobID     string
	planName  string

	ctxFiles []string // prefix document file paths, uploaded once (in order)
	tmpFiles []string // temp files this prefix owns and removes on Close

	mu           sync.Mutex
	fileIDs      []string // Files-API IDs, populated by the first request
	uploaded     bool
	firstStarted bool
	ready        chan struct{} // closed once the first request has returned
}

// NewSharedPrefix builds a shared prefix from raw context bytes. The bytes are
// written to a temporary file (removed by Close) and uploaded once as the sole
// prefix document. Use this when the caller already holds the assembled context
// in memory; use NewSharedPrefixFromFiles to upload existing on-disk context
// files (e.g. cx-generated context) without a copy.
func NewSharedPrefix(system string, contextBytes []byte, opts SharedPrefixOptions) (*SharedPrefix, error) {
	f, err := os.CreateTemp("", "grove-fanout-prefix-*.txt")
	if err != nil {
		return nil, fmt.Errorf("creating fan-out prefix temp file: %w", err)
	}
	if _, err := f.Write(contextBytes); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("writing fan-out prefix temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("closing fan-out prefix temp file: %w", err)
	}

	p, err := newSharedPrefix(system, []string{f.Name()}, opts)
	if err != nil {
		_ = os.Remove(f.Name())
		return nil, err
	}
	p.tmpFiles = append(p.tmpFiles, f.Name())
	return p, nil
}

// NewSharedPrefixFromFiles builds a shared prefix from existing context files,
// uploaded once in the given order behind a single breakpoint. The files are
// not owned by the prefix and are left in place by Close.
func NewSharedPrefixFromFiles(system string, contextFiles []string, opts SharedPrefixOptions) (*SharedPrefix, error) {
	if len(contextFiles) == 0 {
		return nil, fmt.Errorf("SharedPrefix requires at least one context file")
	}
	return newSharedPrefix(system, contextFiles, opts)
}

func newSharedPrefix(system string, contextFiles []string, opts SharedPrefixOptions) (*SharedPrefix, error) {
	if opts.Model == "" {
		return nil, fmt.Errorf("SharedPrefix requires a model")
	}
	ttl := opts.TTL
	if ttl == "" {
		ttl = CacheTTL5m
	}
	switch ttl {
	case CacheTTL5m, CacheTTL1h:
	default:
		return nil, fmt.Errorf("invalid TTL %q (valid: %q, %q)", ttl, CacheTTL5m, CacheTTL1h)
	}

	maxTokens := opts.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultFanoutMaxTokens
	}

	client, err := NewClient(opts.APIKey)
	if err != nil {
		return nil, err
	}

	caller := opts.Caller
	if caller == "" {
		caller = anthropiccontext.GetCaller()
	}

	return &SharedPrefix{
		client:    client,
		system:    system,
		model:     models.ResolveAlias(opts.Model),
		ttl:       ttl,
		maxTokens: maxTokens,
		caller:    caller,
		jobID:     opts.JobID,
		planName:  opts.PlanName,
		ctxFiles:  append([]string{}, contextFiles...),
		ready:     make(chan struct{}),
	}, nil
}

// Model returns the resolved full API model ID the prefix runs against.
func (p *SharedPrefix) Model() string { return p.model }

// Request issues one task request against the shared prefix and returns the
// generated text plus its usage (cache_creation_input_tokens /
// cache_read_input_tokens are surfaced on the UsageResult). The first Request
// writes the prefix cache; later Requests block until it has returned, then
// cache-read the prefix. taskPrompt must be non-empty. It is the single-turn
// case of RequestWithHistory (empty history).
func (p *SharedPrefix) Request(ctx context.Context, taskPrompt string) (string, *UsageResult, error) {
	return p.RequestWithHistory(ctx, nil, taskPrompt)
}

// RequestWithHistory issues a task request that replays prior conversation
// turns between the cached prefix and a new final user turn — the multi-turn
// refinement case. history is the completed dialogue in order, starting with
// the turn-0 user turn (its content is merged with the cached documents into
// the leading user message, so the cached document prefix stays byte-identical
// to a turn-0 Request and is cache-READ within the TTL); taskPrompt is the new
// final user turn. Caching and the first-writer serialization gate work exactly
// as in Request. taskPrompt must be non-empty; empty history ⇒ single-turn.
func (p *SharedPrefix) RequestWithHistory(ctx context.Context, history []MessageTurn, taskPrompt string) (string, *UsageResult, error) {
	if taskPrompt == "" {
		return "", nil, fmt.Errorf("task prompt cannot be empty")
	}

	p.mu.Lock()
	amFirst := !p.firstStarted
	p.firstStarted = true
	p.mu.Unlock()

	if amFirst {
		// The designated writer: upload the prefix + run, then release siblings.
		text, usage, err := p.do(ctx, history, taskPrompt)
		close(p.ready)
		return text, usage, err
	}

	// Wait for the first request's cache write to land so we cache-READ the
	// prefix instead of racing a redundant second WRITE.
	select {
	case <-p.ready:
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
	return p.do(ctx, history, taskPrompt)
}

// buildRequest assembles the ladder GenerateRequest for one task against the
// prefix: every prefix document is a layer (breakpoint on the last), the system
// prompt carries BP1 when present, history replays prior turns (empty ⇒ a plain
// single-turn request), and taskPrompt is the volatile final block.
func (p *SharedPrefix) buildRequest(history []MessageTurn, taskPrompt string) GenerateRequest {
	return GenerateRequest{
		Model:        p.model,
		Prompt:       taskPrompt,
		SystemPrompt: p.system,
		Regions: ContextRegions{
			Layout:     CacheLayoutLadder,
			Files:      p.ctxFiles,
			LayerCount: len(p.ctxFiles),
		},
		History:   history,
		MaxTokens: p.maxTokens,
		CacheTTL:  p.ttl,
	}
}

func (p *SharedPrefix) do(ctx context.Context, history []MessageTurn, taskPrompt string) (string, *UsageResult, error) {
	if err := p.ensureUploaded(ctx); err != nil {
		return "", nil, err
	}

	req := p.buildRequest(history, taskPrompt)
	params := buildMessageParams(req, p.fileIDs)

	startTime := time.Now()
	text, usage, err := p.client.streamMessage(ctx, params)
	logQuery(ctx, startTime, p.model, p.caller, usage, p.ttl, err)

	var usageResult *UsageResult
	if usage != nil {
		usageResult = newUsageResult(p.model, usage, p.ttl)
	}
	if err != nil {
		return "", usageResult, fmt.Errorf("fan-out request failed: %w", err)
	}
	return text, usageResult, nil
}

// ensureUploaded uploads the prefix documents to the Files API exactly once and
// caches their IDs. Guarded by p.mu; the first request populates fileIDs and
// every later request reuses them, keeping the cached prefix byte-identical.
func (p *SharedPrefix) ensureUploaded(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.uploaded {
		return nil
	}
	ids := make([]string, 0, len(p.ctxFiles))
	for _, f := range p.ctxFiles {
		md, err := uploadFile(ctx, &p.client.client, f)
		if err != nil {
			return fmt.Errorf("uploading fan-out prefix document %s: %w", f, err)
		}
		ids = append(ids, md.ID)
	}
	p.fileIDs = ids
	p.uploaded = true
	return nil
}

// Close removes any temp files the prefix owns (those created by
// NewSharedPrefix). Uploaded Files-API documents are left to expire on their
// own. Close is safe to call once the wave is done; it is a no-op for prefixes
// built from caller-owned files.
func (p *SharedPrefix) Close() error {
	var firstErr error
	for _, f := range p.tmpFiles {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	p.tmpFiles = nil
	return firstErr
}

// WorkDirContextFiles returns the cx-generated context document paths that the
// legacy one-shot layout would upload for workDir — cold/cached context, then
// CLAUDE.md, then hot context — skipping missing files and the empty-cold-context
// stub. Callers that have already run `cx generate` in workDir (e.g. docgen's
// BuildContext) use this to feed exactly that context into a SharedPrefix
// without re-implementing cx path resolution. Returns an empty slice when no
// context has been generated.
func WorkDirContextFiles(workDir string) []string {
	ctxMgr := cxcontext.NewManager(workDir)
	hot := ctxMgr.ResolveContextPath()
	cold := ctxMgr.ResolveCachedContextPath()
	regions := assembleContextRegions(RequestOptions{WorkDir: workDir}, workDir, hot, cold)
	return regions.Files
}

// IsAnthropicModel reports whether model (ID or alias) is a Claude model this
// package can serve directly. Re-exported from the models package so fan-out
// callers can gate on it without importing models separately.
func IsAnthropicModel(model string) bool {
	return models.IsAnthropicModel(model)
}

// ResolveModelAlias expands a model alias (e.g. "claude-haiku-4-5") to its full
// API ID, or returns the input unchanged. Re-exported so fan-out callers can
// compare a section's model against SharedPrefix.Model() without importing the
// models package separately.
func ResolveModelAlias(model string) string {
	return models.ResolveAlias(model)
}
