package settings

import (
	"strings"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// knownTools is the set of tool names the Probe page accepts as an explicit
// "Tool: argument" prefix. Anything else is inferred from the input shape.
var knownTools = map[string]struct{}{
	"Bash": {}, "PowerShell": {}, "Read": {}, "Edit": {}, "Write": {},
	"MultiEdit": {}, "NotebookEdit": {}, "Glob": {}, "Grep": {},
	"WebFetch": {}, "WebSearch": {}, "Agent": {}, "Task": {},
}

// inferToolCall turns free-form probe input into a ToolCall. An explicit
// "Tool: arg" prefix (where Tool is a known tool) is honored; otherwise the
// shape decides — a URL becomes WebFetch, a path-like token becomes Read, and
// everything else is treated as a Bash command.
func inferToolCall(input string) ccsettings.ToolCall {
	s := strings.TrimSpace(input)
	if s == "" {
		return ccsettings.ToolCall{Tool: "Bash"}
	}

	if tool, arg, ok := splitToolPrefix(s); ok {
		return buildCall(tool, arg)
	}

	switch {
	case strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "https://"):
		return ccsettings.ToolCall{Tool: "WebFetch", URL: s}
	case looksLikePath(s):
		return ccsettings.ToolCall{Tool: "Read", Path: s}
	default:
		return ccsettings.ToolCall{Tool: "Bash", Command: s}
	}
}

// splitToolPrefix recognizes a leading "Tool:" where Tool is a known tool
// name. It deliberately ignores URLs (which also contain a colon) because
// their scheme is not a known tool.
func splitToolPrefix(s string) (tool, arg string, ok bool) {
	idx := strings.IndexByte(s, ':')
	if idx <= 0 {
		return "", "", false
	}
	name := strings.TrimSpace(s[:idx])
	if _, known := knownTools[name]; !known {
		return "", "", false
	}
	return name, strings.TrimSpace(s[idx+1:]), true
}

func buildCall(tool, arg string) ccsettings.ToolCall {
	c := ccsettings.ToolCall{Tool: tool}
	switch tool {
	case "Bash", "PowerShell":
		c.Command = arg
	case "WebFetch", "WebSearch":
		if strings.Contains(arg, "://") {
			c.URL = arg
		} else {
			c.URL = "https://" + arg
		}
	case "Agent", "Task":
		c.Params = map[string]any{"subagent_type": arg}
	default: // path tools
		c.Path = arg
	}
	return c
}

func looksLikePath(s string) bool {
	if strings.ContainsAny(s, " \t") {
		return false
	}
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "~/") || strings.HasPrefix(s, "../") ||
		strings.Contains(s, "/")
}

// isShadowed reports whether an allow/ask rule is overridden by a deny rule —
// the deny-wins semantics made visible. It synthesizes a representative call
// from the rule's specifier and re-runs the engine; a Deny verdict means the
// rule never takes effect. Returns false (no badge) for rule shapes whose
// specifier cannot be faithfully concretized without the engine's unexported
// path-anchor resolver (bare names and path rules), avoiding false positives.
func isShadowed(engine *ccsettings.Engine, pr ccsettings.ParsedRule) bool {
	call, ok := synthCall(pr)
	if !ok {
		return false
	}
	return engine.Evaluate(call).Result == ccsettings.ResultDeny
}

// synthCall builds a concrete call that the rule's own specifier would match,
// used purely to test whether some deny rule shadows it.
func synthCall(pr ccsettings.ParsedRule) (ccsettings.ToolCall, bool) {
	switch pr.Kind {
	case ccsettings.SpecBash:
		return ccsettings.ToolCall{Tool: pr.Tool, Command: concretize(pr.Specifier)}, true
	case ccsettings.SpecDomain:
		return ccsettings.ToolCall{Tool: pr.Tool, URL: "https://" + concretize(pr.Domain)}, true
	case ccsettings.SpecParam:
		return ccsettings.ToolCall{
			Tool:   pr.Tool,
			Params: map[string]any{pr.ParamName: concretize(pr.ParamValue)},
		}, true
	case ccsettings.SpecLiteral:
		if pr.Tool == "Agent" {
			return ccsettings.ToolCall{
				Tool:   pr.Tool,
				Params: map[string]any{"subagent_type": pr.Specifier},
			}, true
		}
		return ccsettings.ToolCall{}, false
	default:
		// SpecNone (bare tool) and SpecPath (needs anchor resolution).
		return ccsettings.ToolCall{}, false
	}
}

// removesTool reports whether a deny rule removes the whole tool from Claude's
// context (a bare tool name or a tool-name glob), as opposed to blocking only
// matching calls.
func removesTool(pr ccsettings.ParsedRule) bool {
	return pr.Kind == ccsettings.SpecNone || pr.ToolIsGlob
}

// toolOf returns the tool name a rule applies to, for grouping. Unparseable
// rules group under "(invalid)".
func toolOf(rule string) string {
	if pr, ok := ccsettings.ParseRule(rule); ok {
		return pr.Tool
	}
	return "(invalid)"
}
