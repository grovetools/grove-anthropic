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
	p := &sandboxPage{data: d, tv: newTreeView(false)}
	p.tv.setRoots(p.build())
	return p
}

func (p *sandboxPage) Name() string  { return "Sandbox" }
func (p *sandboxPage) TabID() string { return "sandbox" }
func (p *sandboxPage) Title() string {
	return theme.DefaultTheme.Muted.Render("  Directories & sandbox boundary (read / write / network)")
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
			add(fmt.Sprintf("    %s %s", scopeTag(d.Scope), d.Value))
		}
	}
	blank()

	// --- Filesystem boundary ---
	add(section("Filesystem boundary"))
	p.boundarySection(&rows, "Writable", p.data.FS.AllowWrite, p.data.FS.DenyWrite)
	p.boundarySection(&rows, "Readable", p.data.FS.AllowRead, p.data.FS.DenyRead)
	if m.AllowManagedReadPathsOnly {
		add("  " + badge("allowManagedReadPathsOnly", th.Error) + th.Muted.Render(" — only managed allowRead survives"))
	}
	blank()

	// --- Network ---
	add(section("Network"))
	p.domainList(&rows, "allowed", p.data.Net.AllowedDomains, th.Success)
	p.domainList(&rows, "denied", p.data.Net.DeniedDomains, th.Error)
	if p.data.Net.AllowManagedDomainsOnly {
		add("  " + badge("allowManagedDomainsOnly", th.Error) + th.Muted.Render(" — non-allowed domains blocked, not prompted"))
	}
	blank()

	// --- Sandbox mode/flags ---
	add(section("Sandbox"))
	add("  " + kvLine("enabled", boolProv(m.SandboxEnabled)))
	add("  " + kvLine("failIfUnavailable", boolProv(m.SandboxFailIfUnavailable)))
	add("  " + kvLine("allowUnsandboxedCommands", boolProv(m.SandboxAllowUnsandboxedCommands)))
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

func (p *sandboxPage) domainList(rows *[]*node, label string, entries []ccsettings.BoundaryEntry, style interface{ Render(...string) string }) {
	th := theme.DefaultTheme
	if len(entries) == 0 {
		*rows = append(*rows, leaf("  "+kvLine(label+" domains", th.Muted.Render("(none)")), nil))
		return
	}
	*rows = append(*rows, leaf("  "+th.Muted.Render(label+" domains:"), nil))
	for _, e := range entries {
		*rows = append(*rows, leaf(fmt.Sprintf("    %s %s  %s",
			style.Render(e.Path), scopeTag(e.Scope), th.Muted.Render(e.Source)), nil))
	}
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
	if km, ok := msg.(tea.KeyMsg); ok {
		p.tv.handleKey(km)
	}
	return p, nil
}

func (p *sandboxPage) View() string     { return p.tv.view() }
func (p *sandboxPage) Focus() tea.Cmd   { p.tv.active = true; return nil }
func (p *sandboxPage) Blur()            { p.tv.active = false }
func (p *sandboxPage) SetSize(w, h int) { p.tv.setSize(w, h) }
