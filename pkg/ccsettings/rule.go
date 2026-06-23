package ccsettings

import "strings"

// SpecifierKind classifies how a rule's specifier (the part inside the
// parentheses) should be interpreted and matched.
type SpecifierKind int

const (
	SpecNone    SpecifierKind = iota // bare tool name, e.g. "Bash" — matches all uses
	SpecBash                         // Bash/PowerShell command pattern
	SpecPath                         // Read/Edit/Write/... gitignore-anchored path
	SpecDomain                       // WebFetch(domain:...)
	SpecParam                        // Tool(param:value) parameter rule
	SpecLiteral                      // exact-match specifier (e.g. Agent(Explore), mcp tool)
)

// ParsedRule is a classified permission rule string.
type ParsedRule struct {
	Raw          string
	Tool         string // tool name, or a tool-name glob like "mcp__*" / "*"
	ToolIsGlob   bool   // Tool contains a wildcard (only valid for deny/ask)
	HasSpecifier bool
	Specifier    string // raw text inside the parentheses
	Kind         SpecifierKind

	// Populated for SpecParam.
	ParamName  string
	ParamValue string

	// Populated for SpecDomain.
	Domain string
}

// bashParams are non-command Bash input parameters that a Tool(param:value)
// rule may legitimately gate on. The `command` field is excluded by Claude
// (it has its own canonicalizing matcher), so a colon specifier naming any of
// these is a parameter rule rather than a command pattern.
var bashParams = map[string]struct{}{
	"run_in_background": {},
}

// pathTools are the built-in tools whose specifier is a gitignore-anchored path.
var pathTools = map[string]struct{}{
	"Read":         {},
	"Edit":         {},
	"Write":        {},
	"MultiEdit":    {},
	"NotebookEdit": {},
	"Glob":         {},
	"Grep":         {},
}

// shellTools are the tools whose specifier is a shell command pattern.
var shellTools = map[string]struct{}{
	"Bash":       {},
	"PowerShell": {},
}

// ParseRule parses and classifies a rule string such as "Bash(git commit *)",
// "Read(.env)", "WebFetch(domain:example.com)", "mcp__*", or a bare "Bash".
// ok is false for empty or structurally malformed rules.
func ParseRule(raw string) (ParsedRule, bool) {
	r := ParsedRule{Raw: raw}
	s := strings.TrimSpace(raw)
	if s == "" {
		return r, false
	}

	open := strings.IndexByte(s, '(')
	if open < 0 {
		// Bare tool name (possibly a tool-name glob like "mcp__*" or "*").
		r.Tool = s
		r.ToolIsGlob = strings.ContainsRune(s, '*')
		r.Kind = SpecNone
		return r, true
	}
	if open == 0 || !strings.HasSuffix(s, ")") {
		return r, false
	}
	r.Tool = strings.TrimSpace(s[:open])
	r.ToolIsGlob = strings.ContainsRune(r.Tool, '*')
	r.HasSpecifier = true
	r.Specifier = s[open+1 : len(s)-1]

	// "Bash(*)" / "WebFetch(domain:*)" handled as their kinds below; a bare
	// "*" specifier is equivalent to no specifier (matches all uses).
	if r.Specifier == "*" {
		r.HasSpecifier = false
		r.Kind = SpecNone
		return r, true
	}

	r.Kind = classifySpecifier(r.Tool, r.Specifier, &r)
	return r, true
}

// classifySpecifier determines how the specifier should be matched and fills in
// the kind-specific fields of r.
func classifySpecifier(tool, spec string, r *ParsedRule) SpecifierKind {
	switch {
	case tool == "WebFetch":
		if rest, ok := strings.CutPrefix(spec, "domain:"); ok {
			r.Domain = strings.TrimSpace(rest)
			return SpecDomain
		}
		return SpecLiteral

	case isShellTool(tool):
		// A trailing ":*" is an equivalent way to write a trailing " *"
		// wildcard, so it is a command pattern, not a parameter rule.
		if strings.HasSuffix(spec, ":*") {
			return SpecBash
		}
		if name, value, ok := splitParam(spec); ok {
			if _, isParam := bashParams[name]; isParam {
				r.ParamName, r.ParamValue = name, value
				return SpecParam
			}
			// `command:...` and other colon forms are not valid Bash
			// parameter rules; treat as a (likely ineffective) command
			// pattern so the literal text still drives matching.
		}
		return SpecBash

	case isPathTool(tool):
		return SpecPath

	default:
		// Agent(model:opus), generic Tool(param:value), or a literal
		// specifier like Agent(Explore) / an exact MCP tool name.
		if name, value, ok := splitParam(spec); ok {
			r.ParamName, r.ParamValue = name, value
			return SpecParam
		}
		return SpecLiteral
	}
}

// splitParam recognizes a "name:value" specifier where name is a bare
// identifier. Whitespace around the colon is ignored, matching Claude.
func splitParam(spec string) (name, value string, ok bool) {
	idx := strings.IndexByte(spec, ':')
	if idx <= 0 {
		return "", "", false
	}
	name = strings.TrimSpace(spec[:idx])
	value = strings.TrimSpace(spec[idx+1:])
	if !isIdentifier(name) {
		return "", "", false
	}
	return name, value, true
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func isShellTool(tool string) bool {
	_, ok := shellTools[tool]
	return ok
}

func isPathTool(tool string) bool {
	_, ok := pathTools[tool]
	return ok
}
