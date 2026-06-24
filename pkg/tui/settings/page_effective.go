package settings

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/components/pager"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// effectivePage summarizes the resolved configuration: the active defaultMode
// and any mode locks, what the sandbox auto-allows, which tools are removed
// from context, the managed lockdowns in force, rule counts, and a schema-drift
// signal (unknown keys + decode warnings).
type effectivePage struct {
	data *Data
	tv   *treeView
}

var (
	_ pager.Page          = (*effectivePage)(nil)
	_ pager.PageWithTitle = (*effectivePage)(nil)
)

func newEffectivePage(d *Data) *effectivePage {
	p := &effectivePage{data: d, tv: newTreeView(false)}
	p.tv.setRoots(p.build())
	return p
}

func (p *effectivePage) Name() string  { return "Effective" }
func (p *effectivePage) TabID() string { return "effective" }
func (p *effectivePage) Title() string {
	return theme.DefaultTheme.Muted.Render("  Resolved summary of the active environment")
}
func (p *effectivePage) Init() tea.Cmd { return nil }

func (p *effectivePage) build() []*node {
	th := theme.DefaultTheme
	m := p.data.Merged
	var rows []*node
	add := func(s string) { rows = append(rows, leaf(s, nil)) }

	// --- Mode ---
	add(section("Permission mode"))
	mode, modeScope := p.data.Engine.DefaultMode()
	add("  " + kvLine("defaultMode", fmt.Sprintf("%s %s", th.Normal.Render(mode), scopeTag(modeScope))))
	if m.DisableBypassPermissionsMode.Value != "" {
		add("  " + kvLine("disableBypassPermissionsMode",
			fmt.Sprintf("%s %s", m.DisableBypassPermissionsMode.Value, scopeTag(m.DisableBypassPermissionsMode.Scope))))
	}
	if m.DisableAutoMode.Value != "" {
		add("  " + kvLine("disableAutoMode",
			fmt.Sprintf("%s %s", m.DisableAutoMode.Value, scopeTag(m.DisableAutoMode.Scope))))
	}
	if m.SkipDangerousModePermissionPrompt.Set {
		add("  " + kvLine("skipDangerousModePermissionPrompt",
			fmt.Sprintf("%t %s",
				m.SkipDangerousModePermissionPrompt.Value,
				scopeTag(m.SkipDangerousModePermissionPrompt.Scope))))
		if m.SkipDangerousModePermissionPrompt.Value {
			add("  " + th.Muted.Render("bypass-permissions-mode dialog already accepted"))
		}
	}
	add("")

	// --- Sandbox auto-allow ---
	add(section("Sandbox"))
	if m.SandboxEnabled.Set && m.SandboxEnabled.Value {
		add("  " + th.Success.Render("✓ enabled") + " " + scopeTag(m.SandboxEnabled.Scope))
		add("  " + th.Muted.Render("Bash commands that run sandboxed skip the whole-tool prompt"))
		add("  " + th.Muted.Render("(autoAllowBashIfSandboxed); content-scoped ask and deny rules still apply"))
	} else {
		add("  " + th.Muted.Render("disabled — no automatic sandbox allowances"))
	}
	add("")

	// --- Tools removed from context ---
	add(section("Tools removed from context"))
	removed := p.data.Engine.RemovedFromContextTools()
	if len(removed) == 0 {
		add("  " + th.Muted.Render("(none)"))
	} else {
		for _, r := range removed {
			add(fmt.Sprintf("  %s %s %s", th.Error.Render("✗"), r.Rule, scopeTag(r.Scope)))
		}
	}
	add("")

	// --- Managed lockdowns ---
	add(section("Managed lockdowns"))
	locks := 0
	if m.AllowManagedPermissionRulesOnly {
		add("  " + badge("allowManagedPermissionRulesOnly", th.Error) + th.Muted.Render(" — only managed permission rules apply"))
		locks++
	}
	if m.AllowManagedReadPathsOnly {
		add("  " + badge("allowManagedReadPathsOnly", th.Error) + th.Muted.Render(" — only managed read paths apply"))
		locks++
	}
	if m.AllowManagedDomainsOnly {
		add("  " + badge("allowManagedDomainsOnly", th.Error) + th.Muted.Render(" — only managed domains apply"))
		locks++
	}
	if locks == 0 {
		add("  " + th.Muted.Render("(none active)"))
	}
	add("")

	// --- Rule counts ---
	add(section("Rule counts"))
	add("  " + kvLine("deny", fmt.Sprintf("%d", len(m.Deny))))
	add("  " + kvLine("ask", fmt.Sprintf("%d", len(m.Ask))))
	add("  " + kvLine("allow", fmt.Sprintf("%d", len(m.Allow))))
	add("  " + kvLine("additionalDirectories", fmt.Sprintf("%d", len(m.AdditionalDirectories))))
	add("")

	// --- Schema coverage ---
	add(section("Schema coverage"))
	classified, cov := ccsettings.ClassifySources(p.data.Sources)
	add("  " + th.Muted.Render(fmt.Sprintf(
		"%d typed · %d schema-known passthrough · %d unknown (of %d top-level keys)",
		cov.Typed, cov.PassthroughKnown, cov.Unknown, cov.Total)))

	// Schema-known passthrough: keys our model does not type but the vendored
	// schema describes. Surface the schema's type + description so they are no
	// longer opaque.
	hasPassthrough := false
	for _, ck := range classified {
		if ck.Class != ccsettings.ClassPassthroughKnown {
			continue
		}
		if !hasPassthrough {
			add("")
			add("  " + th.Bold.Render("Schema-known passthrough"))
			hasPassthrough = true
		}
		head := "  " + th.Warning.Render(ck.Key)
		if ck.Schema != nil && ck.Schema.Type != "" {
			head += " " + th.Muted.Render("("+ck.Schema.Type+")")
		}
		add(head)
		if ck.Schema != nil {
			if desc := firstLine(ck.Schema.Description); desc != "" {
				add("    " + th.Muted.Render(desc))
			}
			if len(ck.Schema.Enum) > 0 {
				add("    " + th.Muted.Render("enum: "+strings.Join(ck.Schema.Enum, ", ")))
			}
		}
	}

	// Unknown: in neither the model nor the schema — the true forward-compat
	// tail. These still round-trip; they are the drift signal worth watching.
	hasUnknown := false
	for _, ck := range classified {
		if ck.Class != ccsettings.ClassUnknown {
			continue
		}
		if !hasUnknown {
			add("")
			add("  " + th.Bold.Render("Unknown (not in model or schema)"))
			hasUnknown = true
		}
		add("  " + th.Error.Render(ck.Key))
	}

	if !hasPassthrough && !hasUnknown {
		add("  " + th.Success.Render("✓ all keys recognized by the typed model or schema"))
	} else {
		add("")
		add("  " + th.Muted.Render("passthrough/unknown keys round-trip safely; a forward-compat drift signal"))
	}

	// Decode warnings: known keys whose value did not match the expected shape.
	if _, warnings := p.driftCounts(); warnings > 0 {
		add("")
		add("  " + th.Warning.Render(fmt.Sprintf(
			"%d decode warning(s) — a typed key's value did not match its expected shape", warnings)))
	}

	return rows
}

// firstLine returns the first non-empty line of a multi-line schema
// description, trimmed, for a compact one-line render.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// driftCounts totals unknown keys and decode warnings across every parsed scope.
func (p *effectivePage) driftCounts() (unknown, warnings int) {
	for _, sf := range p.data.Sources {
		s := sf.Settings
		if s == nil {
			continue
		}
		unknown += len(s.Unknown)
		warnings += len(s.DecodeWarnings)
		unknown += nestedUnknown(s)
	}
	return unknown, warnings
}

// nestedUnknown counts unknown keys held in the nested permission/sandbox
// objects, which carry their own passthrough maps.
func nestedUnknown(s *ccsettings.Settings) int {
	n := 0
	if s.Permissions != nil {
		n += len(s.Permissions.Unknown)
	}
	if s.Sandbox != nil {
		n += len(s.Sandbox.Unknown)
		if s.Sandbox.Filesystem != nil {
			n += len(s.Sandbox.Filesystem.Unknown)
		}
		if s.Sandbox.Network != nil {
			n += len(s.Sandbox.Network.Unknown)
		}
	}
	return n
}

func (p *effectivePage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	if !p.tv.active {
		return p, nil
	}
	if km, ok := msg.(tea.KeyMsg); ok {
		p.tv.handleKey(km)
	}
	return p, nil
}

func (p *effectivePage) View() string     { return p.tv.view() }
func (p *effectivePage) Focus() tea.Cmd   { p.tv.active = true; return nil }
func (p *effectivePage) Blur()            { p.tv.active = false }
func (p *effectivePage) SetSize(w, h int) { p.tv.setSize(w, h) }
