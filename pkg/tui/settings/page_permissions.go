package settings

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/components/pager"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// permissionsPage is a collapsible tree of permission rules grouped tier →
// tool → rule. Each rule shows its source scope and badges that surface the
// cross-scope semantics the raw JSON hides: managed-lock, removes-from-context,
// and shadowed-by-deny (declared vs effective).
type permissionsPage struct {
	data *Data
	tv   *treeView
}

var (
	_ pager.Page          = (*permissionsPage)(nil)
	_ pager.PageWithTitle = (*permissionsPage)(nil)
)

func newPermissionsPage(d *Data) *permissionsPage {
	p := &permissionsPage{data: d, tv: newTreeView(true)}
	p.tv.setRoots(p.build())
	return p
}

func (p *permissionsPage) Name() string  { return "Permissions" }
func (p *permissionsPage) TabID() string { return "permissions" }
func (p *permissionsPage) Title() string {
	return theme.DefaultTheme.Muted.Render("  allow / ask / deny — deny wins across every scope")
}
func (p *permissionsPage) Init() tea.Cmd { return nil }

// tier bundles a permission tier's rules with its display result for coloring.
type tier struct {
	name   string
	rules  []ccsettings.ProvenancedRule
	result ccsettings.DecisionResult
}

func (p *permissionsPage) build() []*node {
	th := theme.DefaultTheme
	m := p.data.Merged

	tiers := []tier{
		{"Deny", m.Deny, ccsettings.ResultDeny},
		{"Ask", m.Ask, ccsettings.ResultAsk},
		{"Allow", m.Allow, ccsettings.ResultAllow},
	}

	var roots []*node
	for _, t := range tiers {
		label := fmt.Sprintf("%s %s",
			tierGlyph(t.result),
			th.Bold.Render(fmt.Sprintf("%s (%d)", t.name, len(t.rules))),
		)
		if len(t.rules) == 0 {
			roots = append(roots, branch(label+" "+th.Muted.Render("(none)"), true))
			continue
		}
		roots = append(roots, branch(label, false, p.toolGroups(t)...))
	}
	return roots
}

// toolGroups groups a tier's rules by tool name, each tool a fold containing
// its rule rows.
func (p *permissionsPage) toolGroups(t tier) []*node {
	th := theme.DefaultTheme
	byTool := map[string][]ccsettings.ProvenancedRule{}
	for _, r := range t.rules {
		tool := toolOf(r.Rule)
		byTool[tool] = append(byTool[tool], r)
	}

	tools := make([]string, 0, len(byTool))
	for tool := range byTool {
		tools = append(tools, tool)
	}
	sort.Strings(tools)

	var groups []*node
	for _, tool := range tools {
		rules := byTool[tool]
		children := make([]*node, 0, len(rules))
		for _, r := range rules {
			children = append(children, leaf(p.ruleLabel(t, r), nil))
		}
		groups = append(groups, branch(
			fmt.Sprintf("%s %s", th.Normal.Render(tool), th.Muted.Render(fmt.Sprintf("(%d)", len(rules)))),
			false, children...))
	}
	return groups
}

func (p *permissionsPage) ruleLabel(t tier, r ccsettings.ProvenancedRule) string {
	th := theme.DefaultTheme
	parts := []string{scopeTag(r.Scope), r.Rule}

	pr, parsed := ccsettings.ParseRule(r.Rule)
	if !parsed {
		parts = append(parts, th.Error.Render("⚠ unparseable"))
		return strings.Join(parts, " ")
	}

	if r.Scope == ccsettings.ScopeManaged {
		parts = append(parts, badge("managed-lock", th.Error))
	}

	switch t.result {
	case ccsettings.ResultDeny:
		if removesTool(pr) {
			parts = append(parts, badge("removes tool from context", th.Warning))
		}
	case ccsettings.ResultAllow, ccsettings.ResultAsk:
		if isShadowed(p.data.Engine, pr) {
			parts = append(parts, badge("shadowed by deny", th.Error))
		}
	}
	return strings.Join(parts, " ")
}

func (p *permissionsPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	if !p.tv.active {
		return p, nil
	}
	if km, ok := msg.(tea.KeyMsg); ok {
		p.tv.handleKey(km)
	}
	return p, nil
}

func (p *permissionsPage) View() string {
	if len(p.tv.flat) == 0 {
		return emptyBox(p.tv.width, p.tv.height, "No permission rules configured.")
	}
	return p.tv.view()
}

func (p *permissionsPage) Focus() tea.Cmd        { p.tv.active = true; return nil }
func (p *permissionsPage) Blur()                 { p.tv.active = false }
func (p *permissionsPage) SetSize(w, h int)      { p.tv.setSize(w, h) }
func (p *permissionsPage) IsZChordPending() bool { return p.tv.zChordPending() }
