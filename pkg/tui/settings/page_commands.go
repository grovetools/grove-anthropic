package settings

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/components/pager"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// commandsPage is the job-centric command browser and the entry point to the
// generalize→allow loop. It has three modes:
//
//   - modeJobs: pick a plan/job whose commands.jsonl to open (browse the
//     .artifacts dirs reachable from the cwd).
//   - modeList: the recorded commands for the chosen job, collapsed (pre+post
//     by link_id), deduped (xN), and annotated with the live ccsettings verdict,
//     with blocked / would-prompt commands sorted first as allow candidates.
//   - modeLadder: for the selected command, the synthesized rule ladder of
//     increasing generality; picking a rung opens the standard edit overlay to
//     write it into permissions.allow of a chosen scope.
//
// The ladder and add-to-allow reuse the existing editIntent / editOverlay /
// ccsettings writer machinery — this page only turns a command + a chosen rung
// into an ActionAddRule intent targeting permissions.allow.
type commandsPage struct {
	data *Data
	tv   *treeView

	mode commandsMode

	jobs     []jobArtifacts
	active   *jobArtifacts // the opened job, or nil in modeJobs
	commands []command

	ladderFor string // command string the ladder was built for
	ladders   []ccsettings.CommandLadder
	ladderCur int

	width  int
	height int
}

type commandsMode int

const (
	modeJobs commandsMode = iota
	modeList
	modeLadder
)

var (
	_ pager.Page          = (*commandsPage)(nil)
	_ pager.PageWithTitle = (*commandsPage)(nil)
)

func newCommandsPage(d *Data) *commandsPage {
	p := &commandsPage{data: d, tv: newTreeView(true)}
	p.jobs = discoverAllJobArtifacts(workingDir())
	p.mode = modeJobs
	p.rebuild()
	return p
}

// workingDir returns the cwd discovery root, falling back to "." on error.
func workingDir() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func (p *commandsPage) Name() string  { return "Commands" }
func (p *commandsPage) TabID() string { return "commands" }
func (p *commandsPage) Title() string {
	th := theme.DefaultTheme
	switch p.mode {
	case modeList:
		return th.Muted.Render("  recorded commands — blocked/would-prompt first; enter to generalize→allow")
	case modeLadder:
		return th.Muted.Render("  pick a rule breadth — never auto-widened; enter writes it to allow")
	default:
		return th.Muted.Render("  pick a job to inspect the commands its agent ran")
	}
}
func (p *commandsPage) Init() tea.Cmd { return nil }

func (p *commandsPage) rebuild() {
	switch p.mode {
	case modeList:
		p.tv.setRoots(p.buildList())
	case modeLadder:
		p.tv.setRoots(p.buildLadder())
	default:
		p.tv.setRoots(p.buildJobs())
	}
}

// jobPayload / commandPayload / rungPayload mark selectable leaves per mode.
type jobPayload struct{ job jobArtifacts }
type commandPayload struct{ cmd command }
type rungPayload struct{ rung ccsettings.RuleRung }

func (p *commandsPage) buildJobs() []*node {
	th := theme.DefaultTheme
	if len(p.jobs) == 0 {
		return []*node{leaf(th.Muted.Render("No recorded jobs found (no .artifacts/*/commands.jsonl nearby)."), nil)}
	}
	roots := make([]*node, 0, len(p.jobs))
	for _, j := range p.jobs {
		label := fmt.Sprintf("%s  %s",
			th.Normal.Render(j.JobName),
			th.Muted.Render(abbrevPath(j.PlanDir, p.data.Ctx.HomeDir)),
		)
		roots = append(roots, leaf(label, jobPayload{job: j}))
	}
	return roots
}

func (p *commandsPage) buildList() []*node {
	th := theme.DefaultTheme
	if len(p.commands) == 0 {
		return []*node{leaf(th.Muted.Render("No commands recorded for this job."), nil)}
	}
	roots := make([]*node, 0, len(p.commands))
	for _, c := range p.commands {
		roots = append(roots, leaf(p.commandLabel(c), commandPayload{cmd: c}))
	}
	return roots
}

// commandLabel renders a command row: verdict glyph, outcome badge, the xN run
// count, and the command itself (truncated for width).
func (p *commandsPage) commandLabel(c command) string {
	th := theme.DefaultTheme
	parts := []string{decisionStyle(c.Verdict).Render(verdictTag(c.Verdict))}
	parts = append(parts, outcomeBadge(c.Outcome))
	if c.Runs > 1 {
		parts = append(parts, th.Muted.Render(fmt.Sprintf("×%d", c.Runs)))
	}
	parts = append(parts, th.Normal.Render(truncateCmd(c.Command, 80)))
	return strings.Join(parts, " ")
}

func (p *commandsPage) buildLadder() []*node {
	th := theme.DefaultTheme
	header := leaf(th.Muted.Render("Command: ")+th.Normal.Render(truncateCmd(p.ladderFor, 90)), nil)
	roots := []*node{header}
	// Group rungs by subcommand; show a non-selectable note for subcommands that
	// can't be allow-listed (e.g. they contain a shell expansion).
	multi := len(p.ladders) > 1
	for _, l := range p.ladders {
		if multi {
			roots = append(roots, leaf(th.Muted.Render("  "+truncateCmd(l.Subcommand, 80)), nil))
		}
		if l.Note != "" {
			roots = append(roots, leaf(th.Muted.Render("    ⚠ "+l.Note), nil))
			continue
		}
		for _, rung := range l.Rungs {
			label := fmt.Sprintf("%s  %s",
				th.Bold.Render(rung.Rule),
				th.Muted.Render("("+rung.Label+")"),
			)
			roots = append(roots, leaf(label, rungPayload{rung: rung}))
		}
	}
	return roots
}

func (p *commandsPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	if !p.tv.active {
		return p, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}

	switch km.String() {
	case "enter", " ":
		if cmd := p.activate(); cmd != nil {
			return p, cmd
		}
		return p, nil
	case "esc", "backspace", "h", "left":
		if p.goBack() {
			return p, nil
		}
	case "r":
		// Refresh: re-scan jobs (modeJobs) or reload the open job (modeList).
		p.refresh()
		return p, nil
	}

	p.tv.handleKey(km)
	return p, nil
}

// activate handles enter/space per mode: open a job, open a command's ladder,
// or (in the ladder) raise the add-to-allow edit intent. Returns a tea.Cmd when
// an edit intent should be raised, else nil.
func (p *commandsPage) activate() tea.Cmd {
	n := p.tv.selected()
	if n == nil {
		return nil
	}
	switch payload := n.data.(type) {
	case jobPayload:
		p.openJob(payload.job)
		return nil
	case commandPayload:
		p.openLadder(payload.cmd)
		return nil
	case rungPayload:
		return p.addToAllowIntent(payload.rung)
	}
	return nil
}

// openJob loads the chosen job's commands.jsonl and switches to the list.
func (p *commandsPage) openJob(j jobArtifacts) {
	cmds, err := loadCommands(j.CommandsPath, p.data.Engine)
	if err != nil {
		return
	}
	j2 := j
	p.active = &j2
	p.commands = cmds
	p.mode = modeList
	p.tv.cursor = 0
	p.rebuild()
}

// openLadder builds the synthesis ladder for the selected command and switches
// to the ladder view. For a compound command, the ladders of every subcommand
// are concatenated so each subcommand's rules are pickable.
func (p *commandsPage) openLadder(c command) {
	ladders := ccsettings.SynthesizeLadders("Bash", c.Command)
	// Open if there's anything to show — either pickable rungs or an
	// explanatory note (e.g. a subcommand that can't be allow-listed).
	any := false
	for _, l := range ladders {
		if len(l.Rungs) > 0 || l.Note != "" {
			any = true
			break
		}
	}
	if !any {
		return
	}
	p.ladderFor = c.Command
	p.ladders = ladders
	p.mode = modeLadder
	p.tv.cursor = 0
	p.rebuild()
}

// addToAllowIntent raises the standard edit overlay to write the chosen rung
// into permissions.allow of a user-selected scope. The ccsettings writer
// re-validates the candidate document through Evaluate before any disk write,
// and the Managed scope is excluded from the overlay's scope cycle.
func (p *commandsPage) addToAllowIntent(rung ccsettings.RuleRung) tea.Cmd {
	rule := rung.Rule
	intent := editIntent{
		kind:           editAddRule,
		title:          fmt.Sprintf("Add %q to allow", rule),
		suggestedScope: writableScopes[0],
		build: func(scope ccsettings.Scope, _ string) ccsettings.Action {
			return ccsettings.Action{
				Kind:   ccsettings.ActionAddRule,
				Rule:   rule,
				ToTier: ccsettings.TierAllow,
			}
		},
	}
	return func() tea.Msg { return editRequestMsg{intent: intent} }
}

// goBack pops one mode level (ladder→list→jobs). Returns false in modeJobs so
// the key falls through (e.g. esc/back is handled elsewhere).
func (p *commandsPage) goBack() bool {
	switch p.mode {
	case modeLadder:
		p.mode = modeList
		p.tv.cursor = 0
		p.rebuild()
		return true
	case modeList:
		p.mode = modeJobs
		p.active = nil
		p.tv.cursor = 0
		p.rebuild()
		return true
	}
	return false
}

// refresh re-scans the job list (modeJobs) or reloads the open job from disk
// (modeList / modeLadder fall back to the list), so a freshly-run command shows
// up without leaving the page.
func (p *commandsPage) refresh() {
	if p.mode == modeJobs {
		p.jobs = discoverAllJobArtifacts(workingDir())
		p.rebuild()
		return
	}
	if p.active != nil {
		if cmds, err := loadCommands(p.active.CommandsPath, p.data.Engine); err == nil {
			p.commands = cmds
		}
		if p.mode == modeLadder {
			p.mode = modeList
		}
		p.rebuild()
	}
}

func (p *commandsPage) View() string {
	if len(p.tv.flat) == 0 {
		return emptyBox(p.tv.width, p.tv.height, "No commands to show.")
	}
	return p.tv.view()
}

func (p *commandsPage) Focus() tea.Cmd        { p.tv.active = true; return nil }
func (p *commandsPage) Blur()                 { p.tv.active = false }
func (p *commandsPage) SetSize(w, h int)      { p.width, p.height = w, h; p.tv.setSize(w, h) }
func (p *commandsPage) IsZChordPending() bool { return p.tv.zChordPending() }

// verdictTag is a compact verdict label for a command row.
func verdictTag(r ccsettings.DecisionResult) string {
	switch r {
	case ccsettings.ResultAllow:
		return "✓ allowed"
	case ccsettings.ResultDeny:
		return "✗ denied"
	case ccsettings.ResultAsk:
		return "? ask"
	default:
		return "· prompt"
	}
}

// outcomeBadge renders the recorded run outcome of a command.
func outcomeBadge(outcome string) string {
	th := theme.DefaultTheme
	switch outcome {
	case outcomeBlocked:
		return badge("blocked", th.Error)
	case outcomeRanError:
		return badge("error", th.Warning)
	case outcomePending:
		return badge("pending", th.Muted)
	default: // ran_ok
		return badge("ok", th.Success)
	}
}

// truncateCmd shortens a command for single-line display, appending an ellipsis
// when cut.
func truncateCmd(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
