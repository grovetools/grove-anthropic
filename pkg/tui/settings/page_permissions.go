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
	rt     ccsettings.RuleTier
}

func (p *permissionsPage) build() []*node {
	th := theme.DefaultTheme
	m := p.data.Merged

	tiers := []tier{
		{"Deny", m.Deny, ccsettings.ResultDeny, ccsettings.TierDeny},
		{"Ask", m.Ask, ccsettings.ResultAsk, ccsettings.TierAsk},
		{"Allow", m.Allow, ccsettings.ResultAllow, ccsettings.TierAllow},
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
			children = append(children, leaf(p.ruleLabel(t, r), rulePayload{
				rule:  r.Rule,
				tier:  t.rt,
				scope: r.Scope,
			}))
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
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	// Edit chords act on the selected rule leaf before the tree consumes the key.
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

// intentForSelected builds an edit intent for the rule under the cursor. remove
// selects the delete intent; otherwise the allow→ask→deny toggle. Returns
// ok=false when the cursor is not on a rule leaf.
func (p *permissionsPage) intentForSelected(remove bool) (editIntent, bool) {
	n := p.tv.selected()
	if n == nil {
		return editIntent{}, false
	}
	rp, ok := n.data.(rulePayload)
	if !ok {
		return editIntent{}, false
	}

	// Guardrail: a managed-scope rule, or any rule while the managed
	// permission-rules-only lockdown is in force, is read-only.
	if rp.scope == ccsettings.ScopeManaged {
		return editIntent{
			kind:     editToggleRule,
			title:    "Permission rule (read-only)",
			readOnly: true,
			reason:   "Managed-scope rules are policy-owned and cannot be edited here.",
		}, true
	}
	if p.data.Merged.AllowManagedPermissionRulesOnly {
		return editIntent{
			kind:     editToggleRule,
			title:    "Permission rule (read-only)",
			readOnly: true,
			reason:   "allowManagedPermissionRulesOnly is set — only managed permission rules apply.",
		}, true
	}

	if remove {
		fromTier := rp.tier
		ruleStr := rp.rule
		return editIntent{
			kind:           editRemoveRule,
			title:          fmt.Sprintf("Remove rule %q", ruleStr),
			suggestedScope: defaultTargetScope(rp.scope),
			build: func(scope ccsettings.Scope, _ string) ccsettings.Action {
				return ccsettings.Action{Kind: ccsettings.ActionRemoveRule, Rule: ruleStr, FromTier: fromTier}
			},
		}, true
	}

	fromTier := rp.tier
	toTier := ccsettings.NextTier(fromTier)
	ruleStr := rp.rule
	return editIntent{
		kind:           editToggleRule,
		title:          fmt.Sprintf("Move rule %q: %s → %s", ruleStr, fromTier, toTier),
		suggestedScope: defaultTargetScope(rp.scope),
		build: func(scope ccsettings.Scope, _ string) ccsettings.Action {
			return ccsettings.Action{
				Kind:     ccsettings.ActionMoveRule,
				Rule:     ruleStr,
				FromTier: fromTier,
				ToTier:   toTier,
			}
		},
	}, true
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
