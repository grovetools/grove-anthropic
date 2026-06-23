package settings

import (
	"fmt"

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

	// --- Schema drift ---
	add(section("Schema coverage"))
	unknown, warnings := p.driftCounts()
	if unknown == 0 && warnings == 0 {
		add("  " + th.Success.Render("✓ all keys recognized by the typed model"))
	} else {
		if unknown > 0 {
			add("  " + th.Warning.Render(fmt.Sprintf("%d unknown key(s) preserved as passthrough", unknown)))
		}
		if warnings > 0 {
			add("  " + th.Warning.Render(fmt.Sprintf("%d decode warning(s) — value did not match expected shape", warnings)))
		}
		add("  " + th.Muted.Render("unknown keys round-trip safely; this is a forward-compat drift signal"))
	}

	return rows
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
