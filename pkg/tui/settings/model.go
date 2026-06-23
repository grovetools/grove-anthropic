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

// Model is the read-only Claude Code settings browser. It hosts the six
// analytical pages in a core pager.Model and overlays a help view. It is a
// standard embeddable model: quit emits embed.CloseRequestMsg and jump-to-source
// emits embed.EditRequestMsg, both handled by embed.RunStandalone.
type Model struct {
	pager pager.Model
	keys  keymap.Base
	help  help.Model
	data  *Data

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
		m.pager, cmd = m.pager.Update(msg)
		return m, cmd

	case embed.FocusMsg, embed.BlurMsg:
		m.pager, cmd = m.pager.Update(msg)
		return m, cmd

	case embed.SetWorkspaceMsg:
		return m, nil

	case tea.KeyMsg:
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
	m.pager.SetFooter(m.footer())
	return m.pager.View()
}

func (m Model) footer() string {
	th := theme.DefaultTheme
	hints := []string{"j/k move", "[/] tabs", "1-6 jump"}
	if _, ok := m.pager.Active().(*scopesPage); ok {
		hints = append(hints, "enter open file")
	}
	if _, ok := m.pager.Active().(*probePage); ok {
		hints = append(hints, "i edit · esc done")
	}
	hints = append(hints, "? help", "q quit")
	return th.Muted.Render(strings.Join(hints, "  "+theme.IconBullet+"  "))
}
