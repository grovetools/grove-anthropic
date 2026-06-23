package settings

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// scopeDisplayOrder is the personal→committed→machine-local→transient reading
// order the UI presents scopes in (Managed first as the read-only ceiling),
// distinct from the deny-first precedence the engine evaluates with.
var scopeDisplayOrder = []ccsettings.Scope{
	ccsettings.ScopeManaged,
	ccsettings.ScopeUser,
	ccsettings.ScopeProject,
	ccsettings.ScopeLocal,
	ccsettings.ScopeCLI,
}

// scopeStyle tints a scope label so provenance reads at a glance: Managed is
// the locked ceiling (error/red), the rest descend in emphasis.
func scopeStyle(s ccsettings.Scope) lipgloss.Style {
	th := theme.DefaultTheme
	switch s {
	case ccsettings.ScopeManaged:
		return th.Error
	case ccsettings.ScopeUser:
		return th.Info
	case ccsettings.ScopeProject:
		return th.Success
	case ccsettings.ScopeLocal:
		return th.Warning
	default: // CLI
		return th.Highlight
	}
}

// scopeTag renders a bracketed, colored scope label, e.g. "[Project]".
func scopeTag(s ccsettings.Scope) string {
	return scopeStyle(s).Render("[" + s.Label() + "]")
}

// decisionStyle maps an evaluation verdict to a themed style.
func decisionStyle(r ccsettings.DecisionResult) lipgloss.Style {
	th := theme.DefaultTheme
	switch r {
	case ccsettings.ResultAllow:
		return th.Success
	case ccsettings.ResultDeny:
		return th.Error
	case ccsettings.ResultAsk:
		return th.Warning
	default: // prompt
		return th.Muted
	}
}

// decisionGlyph returns a compact icon + label for a verdict.
func decisionGlyph(r ccsettings.DecisionResult) string {
	switch r {
	case ccsettings.ResultAllow:
		return "✓ allow"
	case ccsettings.ResultDeny:
		return "✗ deny"
	case ccsettings.ResultAsk:
		return "? ask"
	default:
		return "· prompt"
	}
}

// tierGlyph returns the colored single-letter tier marker used in row labels.
func tierGlyph(result ccsettings.DecisionResult) string {
	switch result {
	case ccsettings.ResultDeny:
		return theme.DefaultTheme.Error.Render("D")
	case ccsettings.ResultAsk:
		return theme.DefaultTheme.Warning.Render("?")
	default:
		return theme.DefaultTheme.Success.Render("A")
	}
}

// badge renders a muted bracketed annotation, e.g. "shadowed", "managed-lock".
func badge(text string, style lipgloss.Style) string {
	return style.Render("⟨" + text + "⟩")
}

// existsGlyph renders a present/absent indicator for a scope file.
func existsGlyph(exists bool) string {
	th := theme.DefaultTheme
	if exists {
		return th.Success.Render("●")
	}
	return th.Muted.Render("○")
}

// kvLine renders an aligned "label: value" informational row.
func kvLine(label, value string) string {
	th := theme.DefaultTheme
	return fmt.Sprintf("%s %s", th.Muted.Render(label+":"), value)
}

// section renders a bold section header line.
func section(title string) string {
	return theme.DefaultTheme.Bold.Render(title)
}

// abbrevPath collapses the home-directory prefix to "~" for compact display.
func abbrevPath(path, home string) string {
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// concretize turns a wildcard pattern into a representative concrete string by
// replacing each run of '*' with a placeholder token, so an allow/ask rule's
// specifier can be fed back through the engine to detect shadowing.
func concretize(pattern string) string {
	var b strings.Builder
	prevStar := false
	for _, r := range pattern {
		if r == '*' {
			if !prevStar {
				b.WriteString("x")
			}
			prevStar = true
			continue
		}
		prevStar = false
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "x"
	}
	return out
}
