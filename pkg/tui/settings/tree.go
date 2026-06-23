package settings

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/theme"
)

// node is a row in a collapsible tree. Leaf rows have no children; container
// rows fold their subtree. The label is pre-styled; data carries a per-page
// payload (e.g. a *ccsettings.SourceFile) used by enter-actions.
type node struct {
	label     string
	children  []*node
	collapsed bool
	data      any
	depth     int // set during rebuild from the parent chain
}

func (n *node) expandable() bool { return len(n.children) > 0 }

// treeView is the reusable scroll+fold+cursor widget every list/tree page
// embeds. showCursor distinguishes selectable pages (Scopes, Permissions,
// Matrix) from purely informational ones (Sandbox, Effective) that scroll but
// have no actionable rows.
type treeView struct {
	vp         viewport.Model
	roots      []*node
	flat       []*node
	cursor     int
	width      int
	height     int
	active     bool
	showCursor bool

	lastZPress time.Time
	lastGPress time.Time
}

func newTreeView(showCursor bool) *treeView {
	return &treeView{vp: viewport.New(0, 0), showCursor: showCursor}
}

// setRoots replaces the tree and rebuilds the visible row list, keeping the
// cursor in range.
func (t *treeView) setRoots(roots []*node) {
	t.roots = roots
	t.rebuild()
	if t.cursor >= len(t.flat) {
		t.cursor = 0
	}
	t.render()
}

func (t *treeView) rebuild() {
	t.flat = t.flat[:0]
	var walk func(ns []*node, depth int)
	walk = func(ns []*node, depth int) {
		for _, n := range ns {
			n.depth = depth
			t.flat = append(t.flat, n)
			if n.expandable() && !n.collapsed {
				walk(n.children, depth+1)
			}
		}
	}
	walk(t.roots, 0)
}

// selected returns the row under the cursor, or nil.
func (t *treeView) selected() *node {
	if t.cursor >= 0 && t.cursor < len(t.flat) {
		return t.flat[t.cursor]
	}
	return nil
}

func (t *treeView) setSize(w, h int) {
	t.width = w
	t.height = h
	vh := h
	if vh < 1 {
		vh = 1
	}
	t.vp.Width = w
	t.vp.Height = vh
	t.render()
}

// handleKey processes navigation and fold chords. It reports handled=false for
// keys it does not consume so the embedding page can act on them (e.g. enter).
func (t *treeView) handleKey(msg tea.KeyMsg) (handled bool) {
	k := msg.String()

	// vim fold chords: z then R/M/o/c.
	if k == "z" {
		t.lastZPress = time.Now()
		return true
	}
	if time.Since(t.lastZPress) < 500*time.Millisecond {
		switch k {
		case "R", "shift+r":
			setCollapsedAll(t.roots, false)
			t.rebuild()
			t.render()
			t.lastZPress = time.Time{}
			return true
		case "M", "shift+m":
			setCollapsedAll(t.roots, true)
			t.cursor = 0
			t.rebuild()
			t.render()
			t.lastZPress = time.Time{}
			return true
		case "o":
			if n := t.selected(); n != nil && n.expandable() && n.collapsed {
				n.collapsed = false
				t.rebuild()
				t.render()
			}
			t.lastZPress = time.Time{}
			return true
		case "c":
			if n := t.selected(); n != nil && n.expandable() && !n.collapsed {
				n.collapsed = true
				t.rebuild()
				t.render()
			}
			t.lastZPress = time.Time{}
			return true
		}
	}

	if k == "g" {
		if time.Since(t.lastGPress) < 500*time.Millisecond {
			if t.showCursor {
				t.cursor = 0
				t.render()
			} else {
				t.vp.GotoTop()
			}
			t.lastGPress = time.Time{}
			return true
		}
		t.lastGPress = time.Now()
		return true
	}

	// Informational pages (no cursor) scroll the viewport directly.
	if !t.showCursor {
		switch k {
		case "up", "k":
			t.vp.LineUp(1)
			return true
		case "down", "j":
			t.vp.LineDown(1)
			return true
		case "G":
			t.vp.GotoBottom()
			return true
		case "ctrl+u", "pgup", "ctrl+b":
			t.vp.HalfViewUp()
			return true
		case "ctrl+d", "pgdown", "ctrl+f":
			t.vp.HalfViewDown()
			return true
		}
		return false
	}

	switch k {
	case "up", "k":
		t.move(-1)
		return true
	case "down", "j":
		t.move(1)
		return true
	case "G":
		if len(t.flat) > 0 {
			t.cursor = len(t.flat) - 1
			t.render()
		}
		return true
	case "ctrl+u", "pgup", "ctrl+b":
		t.move(-t.pageStep())
		return true
	case "ctrl+d", "pgdown", "ctrl+f":
		t.move(t.pageStep())
		return true
	case "enter", " ", "right", "l":
		if n := t.selected(); n != nil && n.expandable() {
			if k == "right" || k == "l" {
				n.collapsed = false
			} else {
				n.collapsed = !n.collapsed
			}
			t.rebuild()
			t.render()
			return true
		}
		// Leaf enter/space falls through so the page can act on it.
		return false
	case "left", "h":
		if n := t.selected(); n != nil && n.expandable() && !n.collapsed {
			n.collapsed = true
			t.rebuild()
			t.render()
		}
		return true
	}
	return false
}

func (t *treeView) pageStep() int {
	if t.height > 1 {
		return t.height / 2
	}
	return 1
}

func (t *treeView) move(delta int) {
	t.cursor += delta
	if t.cursor < 0 {
		t.cursor = 0
	}
	if t.cursor >= len(t.flat) {
		t.cursor = len(t.flat) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
	t.render()
}

func (t *treeView) render() {
	if len(t.flat) == 0 {
		t.vp.SetContent("")
		return
	}
	th := theme.DefaultTheme
	var lines []string
	for i, n := range t.flat {
		indent := strings.Repeat("  ", n.depth)

		indicator := "  "
		if n.expandable() {
			if n.collapsed {
				indicator = "▶ "
			} else {
				indicator = "▼ "
			}
		} else if n.depth > 0 {
			indicator = "• "
		}

		cursor := "  "
		if t.showCursor && i == t.cursor {
			cursor = th.Highlight.Render("❯ ")
		}

		label := n.label
		if t.showCursor && i == t.cursor {
			label = th.Bold.Render(label)
		}
		lines = append(lines, cursor+indent+th.Muted.Render(indicator)+label)
	}
	t.vp.SetContent(strings.Join(lines, "\n"))

	// Center the cursor in the viewport when selectable.
	if t.showCursor {
		target := t.cursor - t.vp.Height/2
		if target < 0 {
			target = 0
		}
		max := len(t.flat) - t.vp.Height
		if max < 0 {
			max = 0
		}
		if target > max {
			target = max
		}
		t.vp.SetYOffset(target)
	}
}

func (t *treeView) view() string { return t.vp.View() }

func (t *treeView) zChordPending() bool {
	return time.Since(t.lastZPress) < 500*time.Millisecond
}

func setCollapsedAll(ns []*node, collapsed bool) {
	for _, n := range ns {
		if n.expandable() {
			n.collapsed = collapsed
			setCollapsedAll(n.children, collapsed)
		}
	}
}

// leaf builds a non-expandable row.
func leaf(label string, data any) *node { return &node{label: label, data: data} }

// branch builds an expandable row with the given children.
func branch(label string, collapsed bool, children ...*node) *node {
	return &node{label: label, collapsed: collapsed, children: children}
}

// emptyBox renders a centered placeholder when a page has nothing to show.
func emptyBox(width, height int, msg string) string {
	th := theme.DefaultTheme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.Colors.MutedText).
		Padding(1, 2).
		Align(lipgloss.Center).
		Render(th.Muted.Render(msg))
	h := height
	if h < 1 {
		h = 1
	}
	return lipgloss.Place(width, h, lipgloss.Center, lipgloss.Center, box)
}
