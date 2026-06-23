package settings

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// This file is the data layer for the Commands page: it reads a job's
// commands.jsonl (written by the hooks command recorder), collapses the paired
// pre/post rows into one logical command with a final outcome, dedups identical
// commands into xN counts (mirroring the session browser's normalizeSessions),
// and annotates each surviving row with the live ccsettings verdict so the
// high-value allow candidates (blocked / would-prompt) can sort first.
//
// Everything here is a pure function over the parsed rows so it is unit-testable
// in isolation from bubbletea and the filesystem.

// commandRow is one JSONL row as written by the hooks command recorder
// (hooks/internal/hooks/command_recorder.go: commandEntry). Only the fields the
// viewer needs are decoded; unknown fields are ignored.
type commandRow struct {
	Timestamp   string   `json:"timestamp"`
	Phase       string   `json:"phase"` // "pre" | "post"
	LinkID      string   `json:"link_id"`
	ToolUseID   string   `json:"tool_use_id"`
	Command     string   `json:"command"`
	Cwd         string   `json:"cwd"`
	Subcommands []string `json:"subcommands"`
	Outcome     string   `json:"outcome"`
	DurationMs  int64    `json:"duration_ms"`
}

// Outcome constants mirror the recorder's vocabulary so the viewer can derive
// the "blocked" state (a pre row with no matching post row).
const (
	outcomePending  = "pending"
	outcomeRanOK    = "ran_ok"
	outcomeRanError = "ran_error"
	outcomeBlocked  = "blocked"
)

// command is one collapsed logical command: the pre and post rows sharing a
// link_id folded into a single entry with the final outcome, plus the run count
// after identical-command dedup and the live engine verdict.
type command struct {
	Command  string
	Cwd      string
	Outcome  string // ran_ok | ran_error | blocked | pending
	Runs     int    // identical-command occurrences folded in (>= 1)
	Verdict  ccsettings.DecisionResult
	Matched  string // matched rule string ("" when none)
	Scope    ccsettings.Scope
	LastSeen string // timestamp of the most recent occurrence
}

// loadCommands reads and parses a commands.jsonl file, collapsing pre/post rows
// and deduping identical commands. The engine annotates each surviving command
// with its verdict. The returned slice is sorted high-value-first: blocked and
// would-prompt commands (the allow candidates) ahead of already-allowed ones.
func loadCommands(path string, engine *ccsettings.Engine) ([]command, error) {
	rows, err := readCommandRows(path)
	if err != nil {
		return nil, err
	}
	collapsed := collapseCommandRows(rows)
	deduped := dedupCommands(collapsed)
	annotateVerdicts(deduped, engine)
	sortCommands(deduped)
	return deduped, nil
}

// readCommandRows reads a JSONL file into commandRow values, skipping blank and
// unparseable lines so a single corrupt row does not abort the whole load.
func readCommandRows(path string) ([]commandRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var rows []commandRow
	sc := bufio.NewScanner(f)
	// Commands can be long; raise the line cap well above the default 64KiB.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r commandRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		rows = append(rows, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

// collapseCommandRows folds the pre and post rows of each command (joined by
// link_id) into one entry with the final outcome. A pre row whose link_id never
// gets a post row is a blocked attempt. Rows without a link_id (older/partial
// data) are passed through individually using their own outcome. Order follows
// first appearance so the most recent commands stay last (the recorder appends
// chronologically).
func collapseCommandRows(rows []commandRow) []command {
	type acc struct {
		idx     int  // position in out, for stable first-seen ordering
		hasPost bool // a post row was seen for this link
	}
	byLink := map[string]*acc{}
	var out []command

	for _, r := range rows {
		if r.LinkID == "" {
			// Unlinked row: emit as its own command with its stated outcome.
			out = append(out, command{
				Command:  r.Command,
				Cwd:      r.Cwd,
				Outcome:  outcomeOf(r),
				Runs:     1,
				LastSeen: r.Timestamp,
			})
			continue
		}
		a, ok := byLink[r.LinkID]
		if !ok {
			out = append(out, command{
				Command:  r.Command,
				Cwd:      r.Cwd,
				Outcome:  outcomeOf(r),
				Runs:     1,
				LastSeen: r.Timestamp,
			})
			byLink[r.LinkID] = &acc{idx: len(out) - 1, hasPost: r.Phase == cmdPhasePost}
			continue
		}
		// A subsequent row for an existing link. The post row carries the
		// authoritative outcome; a later pre row only refreshes metadata.
		c := &out[a.idx]
		if r.Command != "" {
			c.Command = r.Command
		}
		if r.Cwd != "" {
			c.Cwd = r.Cwd
		}
		if r.Timestamp != "" {
			c.LastSeen = r.Timestamp
		}
		if r.Phase == cmdPhasePost {
			a.hasPost = true
			c.Outcome = outcomeOf(r)
		}
	}

	// Any link that only ever saw a pre row (still marked pending here) is a
	// blocked attempt: the command was proposed but never ran.
	for _, a := range byLink {
		if !a.hasPost && out[a.idx].Outcome == outcomePending {
			out[a.idx].Outcome = outcomeBlocked
		}
	}
	return out
}

// outcomeOf returns the outcome recorded on a row, defaulting an empty value to
// pending so a malformed/missing outcome is treated conservatively.
func outcomeOf(r commandRow) string {
	if r.Outcome == "" {
		return outcomePending
	}
	return r.Outcome
}

// cmdPhasePre / cmdPhasePost mirror the recorder's phase vocabulary.
const (
	cmdPhasePre  = "pre"
	cmdPhasePost = "post"
)

// dedupCommands folds identical commands (same command string) into a single
// entry with a run count, like the session browser's normalizeSessions xN
// collapse. The surviving entry keeps the worst outcome seen (blocked beats
// error beats ok) so a command that was sometimes blocked still surfaces as a
// candidate, and the most recent timestamp/cwd. First-seen order is preserved.
func dedupCommands(in []command) []command {
	idxByCmd := map[string]int{}
	var out []command
	for _, c := range in {
		if i, ok := idxByCmd[c.Command]; ok {
			out[i].Runs += c.Runs
			if outcomeSeverity(c.Outcome) > outcomeSeverity(out[i].Outcome) {
				out[i].Outcome = c.Outcome
			}
			if c.LastSeen > out[i].LastSeen {
				out[i].LastSeen = c.LastSeen
				if c.Cwd != "" {
					out[i].Cwd = c.Cwd
				}
			}
			continue
		}
		idxByCmd[c.Command] = len(out)
		out = append(out, c)
	}
	return out
}

// outcomeSeverity ranks outcomes so the "worst" one wins a dedup merge and so
// the most actionable commands (blocked) outrank quietly-successful ones.
func outcomeSeverity(o string) int {
	switch o {
	case outcomeBlocked:
		return 3
	case outcomeRanError:
		return 2
	case outcomePending:
		return 1
	default: // ran_ok
		return 0
	}
}

// annotateVerdicts evaluates each command through the engine and records the
// verdict, matched rule, and deciding scope. A nil engine leaves the verdict at
// its zero value (ResultPrompt).
func annotateVerdicts(cmds []command, engine *ccsettings.Engine) {
	if engine == nil {
		return
	}
	for i := range cmds {
		d := engine.Evaluate(ccsettings.ToolCall{Tool: "Bash", Command: cmds[i].Command})
		cmds[i].Verdict = d.Result
		cmds[i].Matched = d.MatchedRule
		cmds[i].Scope = d.SourceScope
	}
}

// sortCommands orders commands so the high-value allow candidates surface
// first: by verdict priority (deny > prompt > ask > allow — the commands a user
// most likely wants to allow), then by outcome severity (blocked first), then
// most-recent, then alphabetically for a stable tie-break.
func sortCommands(cmds []command) {
	sort.SliceStable(cmds, func(i, j int) bool {
		a, b := cmds[i], cmds[j]
		if pa, pb := verdictPriority(a.Verdict), verdictPriority(b.Verdict); pa != pb {
			return pa > pb
		}
		if sa, sb := outcomeSeverity(a.Outcome), outcomeSeverity(b.Outcome); sa != sb {
			return sa > sb
		}
		if a.LastSeen != b.LastSeen {
			return a.LastSeen > b.LastSeen
		}
		return a.Command < b.Command
	})
}

// verdictPriority ranks verdicts by how interesting they are as allow
// candidates: a command that would prompt or be denied is the one worth a rule;
// an already-allowed command needs no action.
func verdictPriority(r ccsettings.DecisionResult) int {
	switch r {
	case ccsettings.ResultDeny:
		return 3
	case ccsettings.ResultPrompt:
		return 2
	case ccsettings.ResultAsk:
		return 1
	default: // allow
		return 0
	}
}

// jobArtifacts describes one job directory under a plan's .artifacts that has a
// commands.jsonl the viewer can open.
type jobArtifacts struct {
	JobName      string // the .artifacts/<jobName> directory name
	CommandsPath string // absolute path to its commands.jsonl
	PlanDir      string // the plan directory holding .artifacts
}

// discoverJobArtifacts finds every .artifacts/<job>/commands.jsonl reachable
// from start by walking up the directory tree looking for .artifacts dirs (the
// same place the recorder writes them). Results are sorted most-recently-
// modified first so the job a user just ran is at the top.
func discoverJobArtifacts(start string) []jobArtifacts {
	var jobs []jobArtifacts
	seen := map[string]struct{}{}

	dir := start
	for {
		artifactsDir := filepath.Join(dir, ".artifacts")
		if entries, err := os.ReadDir(artifactsDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				cmdPath := filepath.Join(artifactsDir, e.Name(), "commands.jsonl")
				if _, err := os.Stat(cmdPath); err != nil {
					continue
				}
				if _, dup := seen[cmdPath]; dup {
					continue
				}
				seen[cmdPath] = struct{}{}
				jobs = append(jobs, jobArtifacts{
					JobName:      e.Name(),
					CommandsPath: cmdPath,
					PlanDir:      dir,
				})
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	sort.SliceStable(jobs, func(i, j int) bool {
		return jobModTime(jobs[i].CommandsPath) > jobModTime(jobs[j].CommandsPath)
	})
	return jobs
}

func jobModTime(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().UnixNano()
}
