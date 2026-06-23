package settings

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/components/pager"
	"github.com/grovetools/core/tui/theme"
)

// probePage lets the user type a candidate command/path/domain and see the
// live engine verdict — the decision, the matched rule, and the deciding scope
// — so the abstract rule set can be tested against concrete calls.
type probePage struct {
	data    *Data
	input   textinput.Model
	focused bool
	width   int
	height  int
}

var (
	_ pager.Page              = (*probePage)(nil)
	_ pager.PageWithTitle     = (*probePage)(nil)
	_ pager.PageWithTextInput = (*probePage)(nil)
)

func newProbePage(d *Data) *probePage {
	ti := textinput.New()
	ti.Prompt = " ❯ "
	ti.Placeholder = "git push origin main   ·   Read: ~/.env   ·   https://api.example.com"
	ti.CharLimit = 400
	ti.Focus()
	return &probePage{data: d, input: ti, focused: true}
}

func (p *probePage) Name() string  { return "Probe" }
func (p *probePage) TabID() string { return "probe" }
func (p *probePage) Title() string {
	return theme.DefaultTheme.Muted.Render("  Type a candidate call → live allow/ask/deny verdict")
}
func (p *probePage) Init() tea.Cmd { return nil }

// IsTextEntryActive tells the pager to stop intercepting navigation keys while
// the probe input has focus, so digits and [/] land in the field. Esc blurs the
// field to hand navigation back.
func (p *probePage) IsTextEntryActive() bool { return p.focused }

func (p *probePage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	switch km.String() {
	case "esc":
		if p.focused {
			p.focused = false
			p.input.Blur()
		}
		return p, nil
	case "i", "/":
		if !p.focused {
			p.focused = true
			p.input.Focus()
			return p, textinput.Blink
		}
	}
	if !p.focused {
		return p, nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return p, cmd
}

func (p *probePage) View() string {
	th := theme.DefaultTheme
	var b strings.Builder

	b.WriteString(th.Muted.Render("Candidate call:") + "\n")
	b.WriteString(p.input.View() + "\n\n")

	value := strings.TrimSpace(p.input.Value())
	if value == "" {
		b.WriteString(th.Muted.Render("Enter a command, path, or domain to evaluate.\n"))
		b.WriteString(th.Muted.Render("Prefix with a tool (e.g. \"Read: …\", \"WebFetch: …\") to force interpretation."))
		return p.frame(b.String())
	}

	call := inferToolCall(value)
	b.WriteString(section("Interpretation") + "\n")
	b.WriteString("  " + kvLine("tool", th.Normal.Render(call.Tool)) + "\n")
	if call.Command != "" {
		b.WriteString("  " + kvLine("command", call.Command) + "\n")
	}
	if call.Path != "" {
		b.WriteString("  " + kvLine("path", call.Path) + "\n")
	}
	if call.URL != "" {
		b.WriteString("  " + kvLine("url", call.URL) + "\n")
	}
	for k, v := range call.Params {
		b.WriteString("  " + kvLine("param "+k, fmt.Sprintf("%v", v)) + "\n")
	}
	b.WriteString("\n")

	d := p.data.Engine.Evaluate(call)
	b.WriteString(section("Verdict") + "\n")
	b.WriteString("  " + decisionStyle(d.Result).Bold(true).Render(decisionGlyph(d.Result)) + "\n")
	if d.MatchedRule != "" {
		b.WriteString("  " + kvLine("matched rule", d.MatchedRule) + "\n")
		b.WriteString("  " + kvLine("deciding scope", scopeTag(d.SourceScope)) + "\n")
	} else {
		b.WriteString("  " + th.Muted.Render("no rule matched — falls back to the default mode") + "\n")
	}
	if d.RemovedFromContext {
		b.WriteString("  " + badge("tool removed from context", th.Warning) + "\n")
	}

	// For network-bound calls, also surface the sandbox network policy verdict.
	if call.Tool == "WebFetch" && call.URL != "" {
		if host := hostOf(call.URL); host != "" {
			nd := p.data.Net.Decide(host)
			b.WriteString("\n" + section("Network policy") + "\n")
			b.WriteString("  " + kvLine("host", host) + "\n")
			b.WriteString("  " + decisionStyle(nd.Result).Render(decisionGlyph(nd.Result)))
			if nd.MatchedRule != "" {
				b.WriteString(" " + th.Muted.Render("via") + " " + nd.MatchedRule + " " + scopeTag(nd.SourceScope))
			}
			b.WriteString("\n")
		}
	}

	return p.frame(b.String())
}

// frame pads the probe body to the page height so the footer stays put.
func (p *probePage) frame(body string) string {
	return lipgloss.NewStyle().Width(p.width).Height(p.height).MaxHeight(p.height).Render(body)
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

func (p *probePage) Focus() tea.Cmd {
	p.focused = true
	p.input.Focus()
	return textinput.Blink
}

func (p *probePage) Blur() {
	p.focused = false
	p.input.Blur()
}

func (p *probePage) SetSize(w, h int) {
	p.width = w
	p.height = h
	p.input.Width = w - 6
	if p.input.Width < 10 {
		p.input.Width = 10
	}
}
