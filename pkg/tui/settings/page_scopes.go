package settings

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/components/pager"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// scopesPage lists every discovered settings file in precedence order, with
// existence, managed/read-only flags, parse status, and a per-scope rule count.
// Pressing enter on an existing file jumps to it in $EDITOR.
type scopesPage struct {
	data *Data
	tv   *treeView
}

var (
	_ pager.Page          = (*scopesPage)(nil)
	_ pager.PageWithTitle = (*scopesPage)(nil)
)

func newScopesPage(d *Data) *scopesPage {
	p := &scopesPage{data: d, tv: newTreeView(true)}
	p.tv.setRoots(p.build())
	return p
}

func (p *scopesPage) Name() string  { return "Scopes" }
func (p *scopesPage) TabID() string { return "scopes" }
func (p *scopesPage) Title() string {
	return theme.DefaultTheme.Muted.Render("  Settings files, highest precedence first")
}
func (p *scopesPage) Init() tea.Cmd { return nil }

func (p *scopesPage) build() []*node {
	th := theme.DefaultTheme

	// Index discovered sources by scope; present in precedence order.
	byScope := map[ccsettings.Scope]ccsettings.SourceFile{}
	for _, sf := range p.data.Sources {
		byScope[sf.Scope] = sf
	}

	precedence := []ccsettings.Scope{
		ccsettings.ScopeManaged,
		ccsettings.ScopeCLI,
		ccsettings.ScopeLocal,
		ccsettings.ScopeProject,
		ccsettings.ScopeUser,
	}

	var nodes []*node
	for _, sc := range precedence {
		if sc == ccsettings.ScopeCLI {
			// CLI args are transient — no file on disk — but they occupy a
			// fixed precedence rung, so surface the rung explicitly.
			nodes = append(nodes, leaf(fmt.Sprintf("%s %s %s",
				scopeTag(sc),
				th.Muted.Render("(transient session args — no file)"),
				"",
			), nil))
			continue
		}
		sf, ok := byScope[sc]
		if !ok {
			continue
		}
		nodes = append(nodes, leaf(p.rowLabel(sf), sf))
	}
	return nodes
}

func (p *scopesPage) rowLabel(sf ccsettings.SourceFile) string {
	th := theme.DefaultTheme
	if sf.NotInProject {
		return scopeTag(sf.Scope) + " " + th.Muted.Render("(not in a project — no project directory here)")
	}
	var parts []string
	parts = append(parts, scopeTag(sf.Scope), existsGlyph(sf.Exists), abbrevPath(sf.Path, p.data.Ctx.HomeDir))

	if sf.Scope == ccsettings.ScopeManaged {
		parts = append(parts, badge("read-only", th.Error))
	}

	switch {
	case sf.ParseError != nil:
		parts = append(parts, th.Error.Render("⚠ parse error"))
	case !sf.Exists:
		parts = append(parts, th.Muted.Render("(absent)"))
	case sf.Settings != nil:
		if summary := ruleSummary(sf.Settings); summary != "" {
			parts = append(parts, th.Muted.Render("("+summary+")"))
		}
		if n := len(sf.Settings.Unknown); n > 0 {
			parts = append(parts, th.Warning.Render(fmt.Sprintf("%d unknown key(s)", n)))
		}
	}
	return strings.Join(parts, " ")
}

// ruleSummary renders a compact per-file rule/section count.
func ruleSummary(s *ccsettings.Settings) string {
	var bits []string
	if s.Permissions != nil {
		pm := s.Permissions
		if n := len(pm.Allow); n > 0 {
			bits = append(bits, fmt.Sprintf("%d allow", n))
		}
		if n := len(pm.Ask); n > 0 {
			bits = append(bits, fmt.Sprintf("%d ask", n))
		}
		if n := len(pm.Deny); n > 0 {
			bits = append(bits, fmt.Sprintf("%d deny", n))
		}
	}
	if s.Sandbox != nil {
		bits = append(bits, "sandbox")
	}
	if len(s.Env) > 0 {
		bits = append(bits, fmt.Sprintf("%d env", len(s.Env)))
	}
	if len(s.Hooks) > 0 {
		bits = append(bits, "hooks")
	}
	return strings.Join(bits, ", ")
}

func (p *scopesPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	if !p.tv.active {
		return p, nil
	}
	if km, ok := msg.(tea.KeyMsg); ok {
		if p.tv.handleKey(km) {
			return p, nil
		}
		if km.String() == "enter" {
			if n := p.tv.selected(); n != nil {
				if sf, ok := n.data.(ccsettings.SourceFile); ok && sf.Exists {
					path := sf.Path
					return p, func() tea.Msg { return embed.EditRequestMsg{Path: path} }
				}
			}
		}
	}
	return p, nil
}

func (p *scopesPage) View() string {
	if len(p.tv.flat) == 0 {
		return emptyBox(p.tv.width, p.tv.height, "No settings files discovered.")
	}
	return p.tv.view()
}

func (p *scopesPage) Focus() tea.Cmd        { p.tv.active = true; return nil }
func (p *scopesPage) Blur()                 { p.tv.active = false }
func (p *scopesPage) SetSize(w, h int)      { p.tv.setSize(w, h) }
func (p *scopesPage) IsZChordPending() bool { return p.tv.zChordPending() }
