package settings

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// editOverlay is the modal that drives a single edit to completion: pick the
// target scope file (tab cycles User → Project → Local), optionally type a new
// value, review a dry-run diff of the exact bytes that would be written, and
// confirm. It never writes on its own — it returns the prepared ccsettings
// EditPlan to the Model, which commits and reloads.
//
// Guardrail: the Managed scope is absent from the cycle, so it can never be a
// write target; a read-only intent shows its reason and offers no confirm.
type editOverlay struct {
	data   *Data
	intent editIntent

	scope ccsettings.Scope
	input textinput.Model

	// plan is the currently-previewed EditPlan for (scope, value); err holds the
	// validation/guardrail error when preparation failed. Exactly one is set
	// after refresh().
	plan *ccsettings.EditPlan
	err  error

	width  int
	height int
}

func newEditOverlay(data *Data, intent editIntent, w, h int) *editOverlay {
	o := &editOverlay{
		data:   data,
		intent: intent,
		scope:  intent.suggestedScope,
		width:  w,
		height: h,
	}
	if !isWritableScope(o.scope) {
		o.scope = writableScopes[0]
	}
	if intent.needsInput {
		ti := textinput.New()
		ti.Prompt = " ❯ "
		ti.CharLimit = 400
		ti.SetValue(intent.seed)
		ti.Focus()
		ti.Width = w - 8
		if ti.Width < 10 {
			ti.Width = 10
		}
		o.input = ti
	}
	o.refresh()
	return o
}

// value returns the entered text (trimmed) or "" when the intent takes no input.
func (o *editOverlay) value() string {
	if !o.intent.needsInput {
		return ""
	}
	return strings.TrimSpace(o.input.Value())
}

// refresh re-prepares the dry-run plan for the current scope + value, recording
// either a plan or an error for the view to render.
func (o *editOverlay) refresh() {
	o.plan, o.err = nil, nil
	if o.intent.readOnly {
		o.err = fmt.Errorf("%s", o.intent.reason)
		return
	}
	val := o.value()
	if o.intent.needsInput && val == "" {
		// Nothing to preview yet; the view prompts for input.
		return
	}
	action := o.intent.build(o.scope, val)
	plan, err := ccsettings.PrepareEdit(o.data.Merged, o.scope, action)
	if err != nil {
		o.err = err
		return
	}
	o.plan = plan
}

// cycleScope advances the target scope through the writable cycle and re-previews.
func (o *editOverlay) cycleScope() {
	idx := 0
	for i, s := range writableScopes {
		if s == o.scope {
			idx = i
			break
		}
	}
	o.scope = writableScopes[(idx+1)%len(writableScopes)]
	o.refresh()
}

// canCommit reports whether a write is currently possible (a valid plan exists).
func (o *editOverlay) canCommit() bool { return o.plan != nil && o.err == nil }

// Update processes overlay keys. It returns (done, cmd): done=true means the
// overlay should close. A successful commit emits editCommittedMsg via cmd.
func (o *editOverlay) Update(msg tea.KeyMsg) (done bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		return true, nil
	case "tab":
		o.cycleScope()
		return false, nil
	case "shift+tab":
		// reverse cycle for symmetry
		idx := 0
		for i, s := range writableScopes {
			if s == o.scope {
				idx = i
				break
			}
		}
		o.scope = writableScopes[(idx-1+len(writableScopes))%len(writableScopes)]
		o.refresh()
		return false, nil
	case "enter":
		if !o.canCommit() {
			return false, nil
		}
		plan := o.plan
		if err := plan.Commit(); err != nil {
			o.err = err
			o.plan = nil
			return false, nil
		}
		return true, func() tea.Msg { return editCommittedMsg{path: plan.Path} }
	}

	if o.intent.needsInput {
		var c tea.Cmd
		o.input, c = o.input.Update(msg)
		o.refresh()
		return false, c
	}
	return false, nil
}

func (o *editOverlay) View() string {
	th := theme.DefaultTheme
	var b strings.Builder

	b.WriteString(th.Bold.Render(o.intent.title) + "\n\n")

	if o.intent.readOnly {
		b.WriteString(th.Error.Render("Read-only: ") + th.Muted.Render(o.intent.reason) + "\n\n")
		b.WriteString(th.Muted.Render("esc close"))
		return o.frame(b.String())
	}

	// Target-scope chooser.
	b.WriteString(th.Muted.Render("Target file (tab to change):") + "\n  ")
	for i, s := range writableScopes {
		tag := scopeTag(s)
		if s == o.scope {
			tag = th.Highlight.Render("▸") + tag
		} else {
			tag = " " + tag
		}
		b.WriteString(tag)
		if i < len(writableScopes)-1 {
			b.WriteString("  ")
		}
	}
	b.WriteString("\n")
	if path, _ := scopeWritePath(o.data, o.scope); path != "" {
		b.WriteString("  " + th.Muted.Render(abbrevPath(path, o.data.Ctx.HomeDir)) + "\n")
	}
	b.WriteString("\n")

	// Input field (adds).
	if o.intent.needsInput {
		b.WriteString(th.Muted.Render("Value:") + "\n")
		b.WriteString(o.input.View() + "\n\n")
		if o.value() == "" {
			b.WriteString(th.Muted.Render("Type a value, then enter to preview & write."))
			return o.frame(b.String())
		}
	}

	// Dry-run / error.
	if o.err != nil {
		b.WriteString(th.Error.Render("Cannot apply: ") + o.err.Error() + "\n\n")
		b.WriteString(th.Muted.Render("tab change scope  ·  esc cancel"))
		return o.frame(b.String())
	}
	if o.plan == nil {
		b.WriteString(th.Muted.Render("(nothing to preview)"))
		return o.frame(b.String())
	}

	b.WriteString(section("Change") + "\n")
	b.WriteString("  " + o.plan.Action.Describe() + "\n\n")

	b.WriteString(section("Dry run") + "\n")
	if o.plan.Created {
		b.WriteString("  " + th.Warning.Render("creates ") + th.Muted.Render(abbrevPath(o.plan.Path, o.data.Ctx.HomeDir)) + "\n")
	}
	b.WriteString(renderDiff(o.plan.Before, o.plan.After) + "\n")

	b.WriteString(th.Muted.Render("enter write  ·  tab change scope  ·  esc cancel"))
	return o.frame(b.String())
}

func (o *editOverlay) setSize(w, h int) {
	o.width = w
	o.height = h
	if o.intent.needsInput {
		o.input.Width = w - 8
		if o.input.Width < 10 {
			o.input.Width = 10
		}
	}
}

func (o *editOverlay) frame(body string) string {
	th := theme.DefaultTheme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.Colors.MutedText).
		Padding(1, 2).
		Width(min(o.width-4, 100)).
		Render(body)
	return lipgloss.Place(o.width, o.height, lipgloss.Center, lipgloss.Center, box)
}

// scopeWritePath returns the on-disk path the given scope would write to.
func scopeWritePath(data *Data, scope ccsettings.Scope) (string, bool) {
	for _, sf := range data.Sources {
		if sf.Scope == scope {
			return sf.Path, sf.Exists
		}
	}
	return "", false
}

// renderDiff renders a minimal line-oriented diff of before→after so the user
// sees exactly which lines change, with surrounding context proving the rest is
// untouched. Lines present only in after are added (+), only in before removed
// (−), shared lines are context.
func renderDiff(before, after []byte) string {
	th := theme.DefaultTheme
	bl := splitLines(string(before))
	al := splitLines(string(after))

	// Simple LCS-free diff: walk both, matching equal lines greedily. This is
	// sufficient for the small, near-identical settings documents we edit.
	type drow struct {
		sign byte
		text string
	}
	var rows []drow
	i, j := 0, 0
	for i < len(bl) || j < len(al) {
		switch {
		case i < len(bl) && j < len(al) && bl[i] == al[j]:
			rows = append(rows, drow{' ', bl[i]})
			i++
			j++
		case j < len(al) && (i >= len(bl) || !contains(bl[i:], al[j])):
			rows = append(rows, drow{'+', al[j]})
			j++
		case i < len(bl):
			rows = append(rows, drow{'-', bl[i]})
			i++
		default:
			j++
		}
	}

	// Render with a few lines of context around changes.
	var out []string
	for _, r := range rows {
		switch r.sign {
		case '+':
			out = append(out, th.Success.Render("  + "+r.text))
		case '-':
			out = append(out, th.Error.Render("  - "+r.text))
		default:
			out = append(out, th.Muted.Render("    "+r.text))
		}
	}
	if len(out) == 0 {
		return th.Muted.Render("  (no change)")
	}
	return strings.Join(out, "\n")
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func contains(lines []string, target string) bool {
	for _, l := range lines {
		if l == target {
			return true
		}
	}
	return false
}
