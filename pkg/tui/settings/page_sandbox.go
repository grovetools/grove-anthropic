package settings

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/components/pager"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// sandboxPage is an informational, scroll-only view of the effective directory
// and sandbox boundary: working dir + additionalDirectories, the computed
// read/write filesystem boundary, the network domain policy, and the sandbox
// mode/flags. Each computed entry is annotated with the source that produced it.
type sandboxPage struct {
	data *Data
	tv   *treeView
}

var (
	_ pager.Page          = (*sandboxPage)(nil)
	_ pager.PageWithTitle = (*sandboxPage)(nil)
)

func newSandboxPage(d *Data) *sandboxPage {
	// Selectable so directories, sandbox booleans, and domain lists can be
	// edited; the computed boundary rows carry no payload and are skipped.
	p := &sandboxPage{data: d, tv: newTreeView(true)}
	p.tv.setRoots(p.build())
	return p
}

func (p *sandboxPage) Name() string  { return "Sandbox" }
func (p *sandboxPage) TabID() string { return "sandbox" }
func (p *sandboxPage) Title() string {
	return theme.DefaultTheme.Muted.Render("  Directories & sandbox boundary — enter edits · x removes")
}
func (p *sandboxPage) Init() tea.Cmd { return nil }

func (p *sandboxPage) build() []*node {
	th := theme.DefaultTheme
	var rows []*node
	add := func(label string) { rows = append(rows, leaf(label, nil)) }
	blank := func() { add("") }

	m := p.data.Merged

	// --- Directories ---
	add(section("Directories"))
	add("  " + kvLine("working directory", th.Success.Render(p.data.Ctx.CWD)))
	add("  " + kvLine("project root", th.Normal.Render(p.data.Ctx.ProjectRoot)))
	if len(m.AdditionalDirectories) == 0 {
		add("  " + kvLine("additionalDirectories", th.Muted.Render("(none)")))
	} else {
		add("  " + th.Muted.Render("additionalDirectories:"))
		for _, d := range m.AdditionalDirectories {
			rows = append(rows, leaf(
				fmt.Sprintf("    %s %s", scopeTag(d.Scope), d.Value),
				dirPayload{value: d.Value, scope: d.Scope, remove: true},
			))
		}
	}
	rows = append(rows, leaf("  "+th.Highlight.Render("[+ add directory]"), dirPayload{}))
	blank()

	// --- Filesystem boundary ---
	add(section("Filesystem boundary"))
	if p.data.FS.SandboxEnabled {
		add("  " + th.Success.Render("✓ OS-enforced (sandbox enabled)"))
	} else {
		add("  " + th.Muted.Render("sandbox disabled — not OS-enforced; shown as the no-prompt scope"))
	}
	p.boundarySection(&rows, "Writable", p.data.FS.AllowWrite, p.data.FS.DenyWrite)
	p.boundarySection(&rows, "Readable", p.data.FS.AllowRead, p.data.FS.DenyRead)
	if m.AllowManagedReadPathsOnly {
		add("  " + badge("allowManagedReadPathsOnly", th.Error) + th.Muted.Render(" — only managed allowRead survives"))
	}
	blank()

	// --- Network ---
	add(section("Network"))
	p.editableDomains(&rows, "allowed", "allowedDomains", m.NetAllowedDomains, th.Success, p.data.Net.AllowManagedDomainsOnly)
	p.editableDomains(&rows, "denied", "deniedDomains", m.NetDeniedDomains, th.Error, false)
	if p.data.Net.AllowManagedDomainsOnly {
		add("  " + badge("allowManagedDomainsOnly", th.Error) + th.Muted.Render(" — non-allowed domains blocked, not prompted"))
	}
	blank()

	// --- Sandbox mode/flags ---
	add(section("Sandbox"))
	rows = append(rows, p.sandboxBoolRow("enabled", m.SandboxEnabled))
	rows = append(rows, p.sandboxBoolRow("autoAllowBashIfSandboxed", m.SandboxAutoAllowBashIfSandboxed))
	// Show the resolved mode, since autoAllowBashIfSandboxed defaults to true.
	mode := "regular permissions (Bash still prompts)"
	if m.EffectiveAutoAllowBash() {
		mode = "auto-allow (sandboxed Bash runs without prompting)"
	} else if !m.SandboxEnabled.Value {
		mode = "n/a — sandbox disabled"
	}
	add("  " + th.Muted.Render("→ effective Bash mode: "+mode))
	rows = append(rows, p.sandboxBoolRow("failIfUnavailable", m.SandboxFailIfUnavailable))
	rows = append(rows, p.sandboxBoolRow("allowUnsandboxedCommands", m.SandboxAllowUnsandboxedCommands))
	if len(m.SandboxExcludedCommands) == 0 {
		add("  " + kvLine("excludedCommands", th.Muted.Render("(none)")))
	} else {
		add("  " + th.Muted.Render("excludedCommands:"))
		for _, c := range m.SandboxExcludedCommands {
			add(fmt.Sprintf("    %s %s", scopeTag(c.Scope), c.Value))
		}
	}

	return rows
}

func (p *sandboxPage) boundarySection(rows *[]*node, title string, allow, deny []ccsettings.BoundaryEntry) {
	th := theme.DefaultTheme
	*rows = append(*rows, leaf("  "+th.Bold.Render(title), nil))
	if len(allow) == 0 && len(deny) == 0 {
		*rows = append(*rows, leaf("    "+th.Muted.Render("(none)"), nil))
		return
	}
	for _, e := range allow {
		*rows = append(*rows, leaf(p.boundaryRow(th.Success.Render("+"), e), nil))
	}
	for _, e := range deny {
		*rows = append(*rows, leaf(p.boundaryRow(th.Error.Render("−"), e), nil))
	}
}

func (p *sandboxPage) boundaryRow(sign string, e ccsettings.BoundaryEntry) string {
	th := theme.DefaultTheme
	src := th.Muted.Render(e.Source)
	// Implicit defaults (e.g. the working directory) carry no real scope, so
	// only tag entries that came from an actual settings file.
	scopeBit := ""
	if e.Source != "default:workingDirectory" {
		scopeBit = " " + scopeTag(e.Scope)
	}
	return fmt.Sprintf("    %s %s%s  %s", sign, e.Path, scopeBit, src)
}

// editableDomains renders a network domain list (allowedDomains/deniedDomains)
// with per-entry remove payloads and an add affordance. listKey is the JSON key
// ("allowedDomains"/"deniedDomains"); managedOnly suppresses the add affordance
// when the managed-domains-only lockdown makes user additions ineffective.
func (p *sandboxPage) editableDomains(rows *[]*node, label, listKey string, entries []ccsettings.ProvenancedString, style interface{ Render(...string) string }, managedOnly bool) {
	th := theme.DefaultTheme
	if len(entries) == 0 {
		*rows = append(*rows, leaf("  "+kvLine(label+" domains", th.Muted.Render("(none)")), nil))
	} else {
		*rows = append(*rows, leaf("  "+th.Muted.Render(label+" domains:"), nil))
		for _, e := range entries {
			*rows = append(*rows, leaf(
				fmt.Sprintf("    %s %s", style.Render(e.Value), scopeTag(e.Scope)),
				domainPayload{value: e.Value, list: listKey, scope: e.Scope, remove: true},
			))
		}
	}
	if !managedOnly {
		*rows = append(*rows, leaf(
			"  "+th.Highlight.Render(fmt.Sprintf("[+ add %s domain]", label)),
			domainPayload{list: listKey},
		))
	}
}

// sandboxBoolRow renders an editable sandbox boolean with a toggle payload.
func (p *sandboxPage) sandboxBoolRow(key string, b ccsettings.ProvenancedBool) *node {
	return leaf("  "+kvLine(key, boolProv(b)), sandboxBoolPayload{
		key:     key,
		current: b.Value,
		set:     b.Set,
		scope:   b.Scope,
	})
}

// boolProv renders a ProvenancedBool with its deciding scope, or "(unset)".
func boolProv(b ccsettings.ProvenancedBool) string {
	th := theme.DefaultTheme
	if !b.Set {
		return th.Muted.Render("(unset)")
	}
	val := th.Success.Render("true")
	if !b.Value {
		val = th.Warning.Render("false")
	}
	return fmt.Sprintf("%s %s", val, scopeTag(b.Scope))
}

func (p *sandboxPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	if !p.tv.active {
		return p, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	switch km.String() {
	case "enter", " ":
		if intent, ok := p.intentForSelected(false); ok {
			return p, func() tea.Msg { return editRequestMsg{intent: intent} }
		}
	case "x", "d", "delete", "backspace":
		if intent, ok := p.intentForSelected(true); ok {
			return p, func() tea.Msg { return editRequestMsg{intent: intent} }
		}
	}
	p.tv.handleKey(km)
	return p, nil
}

// intentForSelected builds an edit intent for the sandbox row under the cursor.
// remove=true requests deletion of an existing directory/domain entry. Returns
// ok=false on non-editable rows (computed boundaries, headers).
func (p *sandboxPage) intentForSelected(remove bool) (editIntent, bool) {
	n := p.tv.selected()
	if n == nil {
		return editIntent{}, false
	}
	switch pl := n.data.(type) {
	case dirPayload:
		return p.dirIntent(pl, remove)
	case domainPayload:
		return p.domainIntent(pl, remove)
	case sandboxBoolPayload:
		if remove {
			return editIntent{}, false
		}
		return p.sandboxBoolIntent(pl)
	default:
		return editIntent{}, false
	}
}

func (p *sandboxPage) dirIntent(pl dirPayload, remove bool) (editIntent, bool) {
	if pl.remove && remove {
		val := pl.value
		if pl.scope == ccsettings.ScopeManaged {
			return readOnlyIntent("Directory (read-only)", "Managed-scope directories are policy-owned."), true
		}
		return editIntent{
			kind:           editRemoveDirectory,
			title:          fmt.Sprintf("Remove directory %q", val),
			suggestedScope: defaultTargetScope(pl.scope),
			build: func(scope ccsettings.Scope, _ string) ccsettings.Action {
				return ccsettings.Action{Kind: ccsettings.ActionRemoveDirectory, Value: val}
			},
		}, true
	}
	if pl.remove {
		// enter on an existing entry does nothing actionable; ignore.
		return editIntent{}, false
	}
	// The add affordance.
	return editIntent{
		kind:           editAddDirectory,
		title:          "Add additional directory",
		needsInput:     true,
		seed:           "",
		suggestedScope: writableScopes[0],
		build: func(scope ccsettings.Scope, value string) ccsettings.Action {
			return ccsettings.Action{Kind: ccsettings.ActionAddDirectory, Value: value}
		},
	}, true
}

func (p *sandboxPage) domainIntent(pl domainPayload, remove bool) (editIntent, bool) {
	if p.data.Net.AllowManagedDomainsOnly && pl.list == "allowedDomains" {
		return readOnlyIntent("Allowed domains (read-only)",
			"allowManagedDomainsOnly is set — only managed allowed domains apply."), true
	}
	if pl.remove && remove {
		val := pl.value
		list := pl.list
		if pl.scope == ccsettings.ScopeManaged {
			return readOnlyIntent("Domain (read-only)", "Managed-scope domains are policy-owned."), true
		}
		return editIntent{
			kind:           editRemoveDomain,
			title:          fmt.Sprintf("Remove %q from %s", val, list),
			suggestedScope: defaultTargetScope(pl.scope),
			build: func(scope ccsettings.Scope, _ string) ccsettings.Action {
				return ccsettings.Action{Kind: ccsettings.ActionRemoveDomain, Value: val, DomainList: list}
			},
		}, true
	}
	if pl.remove {
		return editIntent{}, false
	}
	list := pl.list
	return editIntent{
		kind:           editAddDomain,
		title:          fmt.Sprintf("Add domain to %s", list),
		needsInput:     true,
		suggestedScope: writableScopes[0],
		build: func(scope ccsettings.Scope, value string) ccsettings.Action {
			return ccsettings.Action{Kind: ccsettings.ActionAddDomain, Value: value, DomainList: list}
		},
	}, true
}

func (p *sandboxPage) sandboxBoolIntent(pl sandboxBoolPayload) (editIntent, bool) {
	if pl.set && pl.scope == ccsettings.ScopeManaged {
		return readOnlyIntent("Sandbox flag (read-only)", "This flag is set by the Managed scope."), true
	}
	key := pl.key
	next := !pl.current // unset is treated as false → toggles to true
	return editIntent{
		kind:           editToggleSandboxBool,
		title:          fmt.Sprintf("Set sandbox.%s = %t", key, next),
		suggestedScope: defaultTargetScope(pl.scope),
		build: func(scope ccsettings.Scope, _ string) ccsettings.Action {
			return ccsettings.Action{Kind: ccsettings.ActionSetSandboxBool, SandboxKey: key, BoolVal: next}
		},
	}, true
}

// readOnlyIntent builds a non-writable intent the overlay renders as a notice.
func readOnlyIntent(title, reason string) editIntent {
	return editIntent{title: title, readOnly: true, reason: reason}
}

func (p *sandboxPage) View() string {
	if len(p.tv.flat) == 0 {
		return emptyBox(p.tv.width, p.tv.height, "No sandbox configuration.")
	}
	return p.tv.view()
}
func (p *sandboxPage) Focus() tea.Cmd        { p.tv.active = true; return nil }
func (p *sandboxPage) Blur()                 { p.tv.active = false }
func (p *sandboxPage) SetSize(w, h int)      { p.tv.setSize(w, h) }
func (p *sandboxPage) IsZChordPending() bool { return p.tv.zChordPending() }
