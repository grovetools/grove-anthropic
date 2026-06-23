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

// matrixPage renders a rule×scope grid: one row per distinct (tier, rule),
// columns for each contributing scope, a "●" where the rule is declared in that
// scope, and a shadowed marker for allow/ask rules a deny overrides.
//
// Per the plan's design note, the analytical views (Matrix, Probe) keep their
// own view state and never share a mutable filter pointer with the Permissions
// tree — hiding rows here must never affect that tree and vice versa. This page
// holds only local state, so that separation is structural.
type matrixPage struct {
	data    *Data
	tv      *treeView
	columns []ccsettings.Scope
}

var (
	_ pager.Page          = (*matrixPage)(nil)
	_ pager.PageWithTitle = (*matrixPage)(nil)
)

func newMatrixPage(d *Data) *matrixPage {
	p := &matrixPage{data: d, tv: newTreeView(true)}
	p.columns = p.presentScopes()
	p.tv.setRoots(p.build())
	return p
}

func (p *matrixPage) Name() string  { return "Matrix" }
func (p *matrixPage) TabID() string { return "matrix" }
func (p *matrixPage) Title() string {
	return theme.DefaultTheme.Muted.Render("  Rule × scope — where each rule is declared")
}
func (p *matrixPage) Init() tea.Cmd { return nil }

// matrixRow is one distinct rule across tiers, with the scopes it is declared
// in and whether it is shadowed.
type matrixRow struct {
	result   ccsettings.DecisionResult
	tier     string
	rule     string
	tool     string
	scopes   map[ccsettings.Scope]bool
	shadowed bool
}

// presentScopes returns the scope columns that contribute at least one rule,
// in display order.
func (p *matrixPage) presentScopes() []ccsettings.Scope {
	seen := map[ccsettings.Scope]bool{}
	mark := func(rules []ccsettings.ProvenancedRule) {
		for _, r := range rules {
			seen[r.Scope] = true
		}
	}
	mark(p.data.Merged.Deny)
	mark(p.data.Merged.Ask)
	mark(p.data.Merged.Allow)

	var cols []ccsettings.Scope
	for _, sc := range scopeDisplayOrder {
		if seen[sc] {
			cols = append(cols, sc)
		}
	}
	return cols
}

func (p *matrixPage) build() []*node {
	th := theme.DefaultTheme
	if len(p.columns) == 0 {
		return nil
	}

	rows := p.collectRows()
	if len(rows) == 0 {
		return nil
	}

	var nodes []*node
	// Column legend header.
	var head strings.Builder
	for _, sc := range p.columns {
		head.WriteString(scopeStyle(sc).Render(scopeInitial(sc)) + " ")
	}
	legend := make([]string, 0, len(p.columns))
	for _, sc := range p.columns {
		legend = append(legend, fmt.Sprintf("%s=%s", scopeInitial(sc), sc.Label()))
	}
	nodes = append(nodes, leaf(fmt.Sprintf("%s %s", head.String(), th.Muted.Render("  "+strings.Join(legend, "  "))), nil))

	for _, r := range rows {
		var cells strings.Builder
		for _, sc := range p.columns {
			if r.scopes[sc] {
				cells.WriteString(scopeStyle(sc).Render("●") + " ")
			} else {
				cells.WriteString(th.Muted.Render("·") + " ")
			}
		}
		label := fmt.Sprintf("%s %s %s", cells.String(), tierGlyph(r.result), r.rule)
		if r.shadowed {
			label += " " + badge("shadowed", th.Error)
		}
		nodes = append(nodes, leaf(label, nil))
	}
	return nodes
}

func (p *matrixPage) collectRows() []matrixRow {
	type key struct {
		tier string
		rule string
	}
	index := map[key]*matrixRow{}
	var order []*matrixRow

	add := func(tier string, result ccsettings.DecisionResult, rules []ccsettings.ProvenancedRule) {
		for _, r := range rules {
			k := key{tier, r.Rule}
			row, ok := index[k]
			if !ok {
				shadowed := false
				if result != ccsettings.ResultDeny {
					if pr, parsed := ccsettings.ParseRule(r.Rule); parsed {
						shadowed = isShadowed(p.data.Engine, pr)
					}
				}
				row = &matrixRow{
					result:   result,
					tier:     tier,
					rule:     r.Rule,
					tool:     toolOf(r.Rule),
					scopes:   map[ccsettings.Scope]bool{},
					shadowed: shadowed,
				}
				index[k] = row
				order = append(order, row)
			}
			row.scopes[r.Scope] = true
		}
	}
	add("deny", ccsettings.ResultDeny, p.data.Merged.Deny)
	add("ask", ccsettings.ResultAsk, p.data.Merged.Ask)
	add("allow", ccsettings.ResultAllow, p.data.Merged.Allow)

	// Stable order: deny → ask → allow, then by tool, then by rule.
	rank := map[string]int{"deny": 0, "ask": 1, "allow": 2}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if rank[a.tier] != rank[b.tier] {
			return rank[a.tier] < rank[b.tier]
		}
		if a.tool != b.tool {
			return a.tool < b.tool
		}
		return a.rule < b.rule
	})

	out := make([]matrixRow, len(order))
	for i, r := range order {
		out[i] = *r
	}
	return out
}

func scopeInitial(s ccsettings.Scope) string {
	label := s.Label()
	if label == "" {
		return "?"
	}
	return label[:1]
}

func (p *matrixPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	if !p.tv.active {
		return p, nil
	}
	if km, ok := msg.(tea.KeyMsg); ok {
		p.tv.handleKey(km)
	}
	return p, nil
}

func (p *matrixPage) View() string {
	if len(p.tv.flat) == 0 {
		return emptyBox(p.tv.width, p.tv.height, "No permission rules to chart.")
	}
	return p.tv.view()
}

func (p *matrixPage) Focus() tea.Cmd        { p.tv.active = true; return nil }
func (p *matrixPage) Blur()                 { p.tv.active = false }
func (p *matrixPage) SetSize(w, h int)      { p.tv.setSize(w, h) }
func (p *matrixPage) IsZChordPending() bool { return p.tv.zChordPending() }
