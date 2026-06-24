package ccsettings

import (
	"path"
	"strconv"
	"strings"
)

// DecisionResult is the outcome of evaluating a tool call against the rules.
type DecisionResult int

const (
	// ResultPrompt means no rule matched, so Claude falls back to its default
	// behavior for the tool (prompt-on-first-use under the default mode).
	ResultPrompt DecisionResult = iota
	ResultDeny
	ResultAsk
	ResultAllow
)

func (r DecisionResult) String() string {
	switch r {
	case ResultDeny:
		return "deny"
	case ResultAsk:
		return "ask"
	case ResultAllow:
		return "allow"
	default:
		return "prompt"
	}
}

// Decision is the resolver's verdict for a tool call.
type Decision struct {
	Result      DecisionResult
	MatchedRule string // the rule string that decided it ("" when no match)
	SourceScope Scope  // scope of the deciding rule
	// RemovedFromContext is set when a bare tool-name (or tool-name glob) deny
	// rule matched: Claude removes the tool from its context entirely rather
	// than just blocking the specific call.
	RemovedFromContext bool
	// Note optionally explains a decision the rules alone don't account for —
	// e.g. a command that prompts because it contains a shell expansion.
	Note string
}

// ToolCall describes a tool invocation to evaluate. Only the fields relevant to
// a given tool need be set.
type ToolCall struct {
	Tool string

	Command string // Bash / PowerShell command string
	Path    string // Read/Edit/Write/Grep/Glob/etc. target path
	// SymlinkTarget, when set, is the path a symlink Path resolves to. Allow
	// rules require BOTH to match; deny rules match if EITHER does.
	SymlinkTarget string
	URL           string // WebFetch URL

	// Params carries the raw top-level tool input for Tool(param:value) rules.
	Params map[string]any
}

type permTier int

const (
	tierDeny permTier = iota
	tierAsk
	tierAllow
)

// provRule is a parsed rule tagged with its source scope.
type provRule struct {
	ParsedRule
	Scope Scope
}

// EngineOptions configures the resolver.
type EngineOptions struct {
	// SandboxAutoAllowBash mirrors autoAllowBashIfSandboxed (default true when
	// the sandbox is enabled): a bare `Bash` ask rule is skipped for commands
	// that run sandboxed, since the sandbox boundary substitutes for the
	// whole-tool prompt. Content-scoped ask rules and deny rules still apply.
	SandboxAutoAllowBash bool
}

// Engine evaluates tool calls against a merged settings view.
type Engine struct {
	merged *MergedSettings
	ctx    ResolveContext
	opts   EngineOptions

	deny  []provRule
	ask   []provRule
	allow []provRule
}

// NewEngine builds an evaluator from a merged settings view. Rules that fail to
// parse are skipped.
func NewEngine(merged *MergedSettings, opts EngineOptions) *Engine {
	e := &Engine{merged: merged, ctx: merged.Context, opts: opts}
	e.deny = parseProvRules(merged.Deny)
	e.ask = parseProvRules(merged.Ask)
	e.allow = parseProvRules(merged.Allow)
	return e
}

func parseProvRules(rules []ProvenancedRule) []provRule {
	out := make([]provRule, 0, len(rules))
	for _, r := range rules {
		if pr, ok := ParseRule(r.Rule); ok {
			out = append(out, provRule{ParsedRule: pr, Scope: r.Scope})
		}
	}
	return out
}

// Evaluate resolves a tool call to a decision, honoring deny → ask → allow
// first-match across scopes.
func (e *Engine) Evaluate(call ToolCall) Decision {
	if isShellTool(call.Tool) {
		return e.evaluateBash(call)
	}
	return e.evaluateSimple(call)
}

// evaluateSimple handles non-shell tools, where the call has a single target.
func (e *Engine) evaluateSimple(call ToolCall) Decision {
	for _, r := range e.deny {
		if e.ruleMatches(r.ParsedRule, call, tierDeny) {
			return Decision{
				Result:             ResultDeny,
				MatchedRule:        r.Raw,
				SourceScope:        r.Scope,
				RemovedFromContext: removesTool(r.ParsedRule),
			}
		}
	}
	for _, r := range e.ask {
		if e.ruleMatches(r.ParsedRule, call, tierAsk) {
			return Decision{Result: ResultAsk, MatchedRule: r.Raw, SourceScope: r.Scope}
		}
	}
	for _, r := range e.allow {
		if e.ruleMatches(r.ParsedRule, call, tierAllow) {
			return Decision{Result: ResultAllow, MatchedRule: r.Raw, SourceScope: r.Scope}
		}
	}
	return Decision{Result: ResultPrompt}
}

// evaluateBash handles the compound-aware Bash/PowerShell semantics: deny if any
// subcommand is denied; ask if any subcommand asks (or a bare-Bash ask applies);
// allow only when every subcommand is covered by some allow rule.
func (e *Engine) evaluateBash(call ToolCall) Decision {
	subs := splitAndStrip(call.Command)

	// DENY
	for _, r := range e.deny {
		if !e.matchTool(r.ParsedRule, call.Tool, tierDeny) {
			continue
		}
		if d, ok := e.bashTierMatch(r, call, subs, true); ok {
			return d
		}
	}

	// ASK
	for _, r := range e.ask {
		if !e.matchTool(r.ParsedRule, call.Tool, tierAsk) {
			continue
		}
		if r.Kind == SpecNone {
			// Bare Bash (or Bash(*)) ask: skipped for sandboxed commands when
			// auto-allow is in effect; still applies to excluded commands.
			if e.opts.SandboxAutoAllowBash && !e.commandIsExcluded(subs) {
				continue
			}
			return Decision{Result: ResultAsk, MatchedRule: r.Raw, SourceScope: r.Scope}
		}
		if d, ok := e.bashTierMatch(r, call, subs, false); ok {
			return d
		}
	}

	// ALLOW: bare-Bash allow grants the whole tool; otherwise every subcommand
	// must be covered by some allow rule.
	for _, r := range e.allow {
		if e.matchTool(r.ParsedRule, call.Tool, tierAllow) && r.Kind == SpecNone {
			return Decision{Result: ResultAllow, MatchedRule: r.Raw, SourceScope: r.Scope}
		}
	}
	// A content-scoped allow rule cannot auto-approve a command containing a
	// shell expansion — Claude prompts regardless ("Contains simple_expansion"),
	// because the expanded text is not knowable from the literal pattern. (A bare
	// whole-tool Bash allow, handled above, still covers it.)
	for _, sub := range subs {
		if ContainsShellExpansion(sub) {
			return Decision{
				Result: ResultPrompt,
				Note:   "contains a shell expansion — not auto-approvable by a content allow rule",
			}
		}
	}
	if rule, scope, ok := e.everySubcommandAllowed(call, subs); ok {
		return Decision{Result: ResultAllow, MatchedRule: rule, SourceScope: scope}
	}

	return Decision{Result: ResultPrompt}
}

// bashTierMatch reports whether a deny/ask Bash rule matches the call. deny=true
// marks a bare-name match as removing the tool from context.
func (e *Engine) bashTierMatch(r provRule, call ToolCall, subs []string, deny bool) (Decision, bool) {
	res := ResultAsk
	if deny {
		res = ResultDeny
	}
	switch r.Kind {
	case SpecNone:
		return Decision{Result: res, MatchedRule: r.Raw, SourceScope: r.Scope, RemovedFromContext: deny}, true
	case SpecParam:
		if matchParam(r.ParsedRule, call) {
			return Decision{Result: res, MatchedRule: r.Raw, SourceScope: r.Scope}, true
		}
	case SpecBash:
		for _, sub := range subs {
			if matchBashPattern(r.Specifier, sub) {
				return Decision{Result: res, MatchedRule: r.Raw, SourceScope: r.Scope}, true
			}
		}
	}
	return Decision{}, false
}

// everySubcommandAllowed reports whether every subcommand is matched by some
// Bash allow rule, returning the rule/scope that covered the first subcommand.
func (e *Engine) everySubcommandAllowed(call ToolCall, subs []string) (string, Scope, bool) {
	if len(subs) == 0 {
		return "", 0, false
	}
	var firstRule string
	var firstScope Scope
	for i, sub := range subs {
		matched := false
		for _, r := range e.allow {
			if !e.matchTool(r.ParsedRule, call.Tool, tierAllow) {
				continue
			}
			if r.Kind == SpecBash && matchBashPattern(r.Specifier, sub) {
				matched = true
				if i == 0 {
					firstRule, firstScope = r.Raw, r.Scope
				}
				break
			}
		}
		if !matched {
			return "", 0, false
		}
	}
	return firstRule, firstScope, true
}

// ruleMatches reports whether a non-shell rule matches the call under the given
// tier (which governs the symlink allow/deny asymmetry for path rules).
func (e *Engine) ruleMatches(r ParsedRule, call ToolCall, tier permTier) bool {
	if !e.matchTool(r, call.Tool, tier) {
		return false
	}
	switch r.Kind {
	case SpecNone:
		return true
	case SpecPath:
		return e.matchPathRule(r, call, tier)
	case SpecDomain:
		return matchDomain(r.Domain, hostFromURL(call.URL))
	case SpecParam:
		return matchParam(r, call)
	case SpecBash:
		return matchBashPattern(r.Specifier, call.Command)
	case SpecLiteral:
		return matchLiteral(r, call)
	default:
		return false
	}
}

// matchPathRule applies the symlink asymmetry: allow rules require the symlink
// path AND its target to match; deny/ask rules match if EITHER does.
func (e *Engine) matchPathRule(r ParsedRule, call ToolCall, tier permTier) bool {
	glob := resolveReadEditAnchor(r.Specifier, e.ctx)
	candidates := []string{call.Path}
	if call.SymlinkTarget != "" {
		candidates = append(candidates, call.SymlinkTarget)
	}
	if tier == tierAllow {
		for _, c := range candidates {
			if !matchPathGlob(glob, c, e.ctx) {
				return false
			}
		}
		return true
	}
	for _, c := range candidates {
		if matchPathGlob(glob, c, e.ctx) {
			return true
		}
	}
	return false
}

// matchTool reports whether the rule's tool-name part matches the called tool,
// applying the allow-tier restriction that tool-name globs are honored only
// after a literal mcp__<server>__ prefix.
func (e *Engine) matchTool(r ParsedRule, tool string, tier permTier) bool {
	if !r.ToolIsGlob {
		return r.Tool == tool
	}
	if tier == tierAllow && !isAnchoredMcpGlob(r.Tool) {
		return false
	}
	ok, _ := path.Match(r.Tool, tool)
	return ok
}

// matchLiteral handles exact-match specifiers, currently Agent(Name).
func matchLiteral(r ParsedRule, call ToolCall) bool {
	if call.Tool == "Agent" {
		for _, key := range []string{"subagent_type", "agent", "name"} {
			if v, ok := call.Params[key]; ok {
				return scalarString(v) == r.Specifier
			}
		}
		return false
	}
	return false
}

// matchParam evaluates a Tool(param:value) rule against the call's params.
func matchParam(r ParsedRule, call ToolCall) bool {
	v, ok := call.Params[r.ParamName]
	if !ok {
		return false // an omitted parameter is never matched
	}
	return matchValueGlob(r.ParamValue, scalarString(v))
}

// removesTool reports whether a deny rule removes the tool from context (a bare
// tool name, Bash(*) form, or a tool-name glob).
func removesTool(r ParsedRule) bool {
	return r.Kind == SpecNone || r.ToolIsGlob
}

// RemovedFromContextTools lists the tools that deny rules remove from Claude's
// context entirely (bare tool-name and tool-name-glob denies).
func (e *Engine) RemovedFromContextTools() []ProvenancedRule {
	var out []ProvenancedRule
	for _, r := range e.deny {
		if removesTool(r.ParsedRule) {
			out = append(out, ProvenancedRule{Rule: r.Raw, Scope: r.Scope})
		}
	}
	return out
}

// DefaultMode returns the resolved defaultMode (falling back to "default").
func (e *Engine) DefaultMode() (string, Scope) {
	if e.merged.DefaultMode.Value != "" {
		return e.merged.DefaultMode.Value, e.merged.DefaultMode.Scope
	}
	return "default", ScopeUser
}

// commandIsExcluded reports whether any subcommand matches a sandbox
// excludedCommands pattern (such commands run unsandboxed).
func (e *Engine) commandIsExcluded(subs []string) bool {
	for _, ex := range e.merged.SandboxExcludedCommands {
		for _, sub := range subs {
			if matchBashPattern(ex.Value, sub) {
				return true
			}
		}
	}
	return false
}

// splitAndStrip splits a compound command and strips process wrappers from each
// subcommand.
func splitAndStrip(cmd string) []string {
	subs := SplitCompound(cmd)
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, StripWrappers(s))
	}
	return out
}

// isAnchoredMcpGlob reports whether a tool-name glob is an allow-eligible
// mcp__<server>__<glob> pattern with a glob-free server segment.
func isAnchoredMcpGlob(tool string) bool {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return false
	}
	idx := strings.Index(rest, "__")
	if idx <= 0 {
		return false
	}
	return !strings.Contains(rest[:idx], "*")
}

// scalarString renders a scalar tool-input value as the literal string Claude
// would compare against (matching its pre-normalization comparison).
func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case nil:
		return ""
	default:
		return ""
	}
}

// matchValueGlob matches a parameter value pattern (with `*` wildcards) against
// an actual value.
func matchValueGlob(pattern, actual string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == actual
	}
	re := bashPatternRegexp(pattern) // reuse: * -> .* with full anchoring
	return re.MatchString(actual)
}
