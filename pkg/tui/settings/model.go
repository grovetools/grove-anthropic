package settings

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/components/help"
	"github.com/grovetools/core/tui/components/pager"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/theme"
)

// Model is the Claude Code settings browser. It hosts the six analytical pages
// in a core pager.Model, overlays a help view, and — for the editable pages —
// an edit overlay that drives scope-targeted, dry-run-confirmed writes through
// the comment-preserving ccsettings writer. It is a standard embeddable model:
// quit emits embed.CloseRequestMsg and jump-to-source emits embed.EditRequestMsg,
// both handled by embed.RunStandalone.
type Model struct {
	pager pager.Model
	keys  keymap.Base
	help  help.Model
	data  *Data

	// edit is the active edit overlay, or nil when no edit is in progress. While
	// non-nil it swallows all input until it closes (confirm or cancel).
	edit *editOverlay

	width  int
	height int
}

// New builds the settings browser model over an already-loaded Data snapshot.
func New(data *Data) Model {
	keys := keymap.NewBase()

	pages := []pager.Page{
		newScopesPage(data),
		newPermissionsPage(data),
		newSandboxPage(data),
		newMatrixPage(data),
		newProbePage(data),
		newEffectivePage(data),
	}

	pgr := pager.NewWith(pages, pager.KeyMapFromBase(keys), pager.Config{
		OuterPadding: [4]int{1, 2, 0, 2},
		ShowTitleRow: true,
		FooterHeight: 1,
	})

	return Model{
		pager: pgr,
		keys:  keys,
		help:  help.NewBuilder().WithKeys(keys).WithTitle("Settings Browser").Build(),
		data:  data,
	}
}

func (m Model) Init() tea.Cmd { return m.pager.Init() }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.help.SetSize(msg.Width, msg.Height)
		if m.edit != nil {
			m.edit.setSize(msg.Width, msg.Height)
		}
		m.pager, cmd = m.pager.Update(msg)
		return m, cmd

	case embed.FocusMsg, embed.BlurMsg:
		m.pager, cmd = m.pager.Update(msg)
		return m, cmd

	case embed.SetWorkspaceMsg:
		return m, nil

	case editRequestMsg:
		// A page asked to edit the selected row: open the overlay over it.
		m.edit = newEditOverlay(m.data, msg.intent, m.width, m.height)
		return m, nil

	case editCommittedMsg:
		// The write succeeded: close the overlay and reload from disk so every
		// page reflects the new settings.
		m.edit = nil
		return m.reload(), nil

	case tea.KeyMsg:
		// The edit overlay, when open, swallows all keys until it closes.
		if m.edit != nil {
			done, c := m.edit.Update(msg)
			if done {
				m.edit = nil
			}
			return m, c
		}

		// Help overlay swallows keys while shown.
		if m.help.ShowAll {
			if key.Matches(msg, m.keys.Help) || key.Matches(msg, m.keys.Back) || key.Matches(msg, m.keys.Quit) {
				m.help.Toggle()
				return m, nil
			}
			m.help, cmd = m.help.Update(msg)
			return m, cmd
		}

		// Let a focused probe input absorb everything, except '?' help toggle.
		if !m.activeTextEntry() {
			if key.Matches(msg, m.keys.Quit) {
				return m, func() tea.Msg { return embed.CloseRequestMsg{} }
			}
			if key.Matches(msg, m.keys.Help) {
				m.help.Toggle()
				return m, nil
			}
		}

		m.pager, cmd = m.pager.Update(msg)
		return m, cmd
	}

	m.pager, cmd = m.pager.Update(msg)
	return m, cmd
}

// reload re-discovers and re-merges settings from disk and rebuilds the pages,
// preserving the active tab and current window size. Called after a successful
// edit write so the browser reflects the change immediately. A reload failure
// leaves the existing Data in place rather than crashing the TUI.
func (m Model) reload() Model {
	data, err := Load()
	if err != nil {
		return m
	}
	active := m.pager.ActiveIndex()
	rebuilt := New(data)
	rebuilt.width = m.width
	rebuilt.height = m.height
	if m.width > 0 && m.height > 0 {
		rebuilt.pager, _ = rebuilt.pager.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	}
	rebuilt.pager.SetActive(active)
	return rebuilt
}

// activeTextEntry reports whether the active page is currently capturing text
// input (the Probe field), so the model defers its global keys to the page.
func (m Model) activeTextEntry() bool {
	if ti, ok := m.pager.Active().(pager.PageWithTextInput); ok {
		return ti.IsTextEntryActive()
	}
	return false
}

func (m Model) View() string {
	if m.help.ShowAll {
		return m.help.View()
	}
	if m.edit != nil {
		return m.edit.View()
	}
	m.pager.SetFooter(m.footer())
	return m.pager.View()
}

func (m Model) footer() string {
	th := theme.DefaultTheme
	hints := []string{"j/k move", "[/] tabs", "1-6 jump"}
	switch m.pager.Active().(type) {
	case *scopesPage:
		hints = append(hints, "enter open file")
	case *probePage:
		hints = append(hints, "i edit · esc done")
	case *permissionsPage:
		hints = append(hints, "enter cycle rule", "x remove")
	case *sandboxPage:
		hints = append(hints, "enter edit", "x remove")
	}
	hints = append(hints, "? help", "q quit")
	return th.Muted.Render(strings.Join(hints, "  "+theme.IconBullet+"  "))
}
