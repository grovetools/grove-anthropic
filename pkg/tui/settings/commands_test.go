package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// findCommand returns the collapsed command with the given command string.
func findCommand(cmds []command, want string) (command, bool) {
	for _, c := range cmds {
		if c.Command == want {
			return c, true
		}
	}
	return command{}, false
}

func TestCollapseCommandRows_PrePostLinkID(t *testing.T) {
	rows := []commandRow{
		// A normal ran-ok command: pre (pending) then post (ran_ok) share a link.
		{Phase: "pre", LinkID: "l1", Command: "ls -la", Outcome: "pending", Timestamp: "t1"},
		{Phase: "post", LinkID: "l1", Command: "ls -la", Outcome: "ran_ok", Timestamp: "t2"},
		// A blocked attempt: pre only, no matching post.
		{Phase: "pre", LinkID: "l2", Command: "rm -rf /", Outcome: "pending", Timestamp: "t3"},
		// A ran-error command.
		{Phase: "pre", LinkID: "l3", Command: "go build", Outcome: "pending", Timestamp: "t4"},
		{Phase: "post", LinkID: "l3", Command: "go build", Outcome: "ran_error", Timestamp: "t5"},
	}

	collapsed := collapseCommandRows(rows)
	if len(collapsed) != 3 {
		t.Fatalf("expected 3 collapsed commands, got %d: %+v", len(collapsed), collapsed)
	}

	ls, ok := findCommand(collapsed, "ls -la")
	if !ok || ls.Outcome != outcomeRanOK {
		t.Errorf("ls -la: ok=%v outcome=%q, want ran_ok", ok, ls.Outcome)
	}
	// The post row's timestamp must win as LastSeen.
	if ls.LastSeen != "t2" {
		t.Errorf("ls -la LastSeen = %q, want t2 (post row)", ls.LastSeen)
	}

	blocked, ok := findCommand(collapsed, "rm -rf /")
	if !ok || blocked.Outcome != outcomeBlocked {
		t.Errorf("rm -rf /: ok=%v outcome=%q, want blocked (pre with no post)", ok, blocked.Outcome)
	}

	errcmd, ok := findCommand(collapsed, "go build")
	if !ok || errcmd.Outcome != outcomeRanError {
		t.Errorf("go build: ok=%v outcome=%q, want ran_error", ok, errcmd.Outcome)
	}
}

func TestDedupCommands_XNCounts(t *testing.T) {
	in := []command{
		{Command: "ls", Outcome: outcomeRanOK, Runs: 1, LastSeen: "t1"},
		{Command: "ls", Outcome: outcomeRanOK, Runs: 1, LastSeen: "t3"},
		{Command: "ls", Outcome: outcomeBlocked, Runs: 1, LastSeen: "t2"},
		{Command: "pwd", Outcome: outcomeRanOK, Runs: 1, LastSeen: "t4"},
	}
	out := dedupCommands(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 deduped commands, got %d: %+v", len(out), out)
	}
	ls, ok := findCommand(out, "ls")
	if !ok {
		t.Fatal("ls not found after dedup")
	}
	if ls.Runs != 3 {
		t.Errorf("ls Runs = %d, want 3", ls.Runs)
	}
	// The worst outcome (blocked) wins the merge so the command still surfaces.
	if ls.Outcome != outcomeBlocked {
		t.Errorf("ls Outcome = %q, want blocked (worst severity)", ls.Outcome)
	}
	// The most recent timestamp is retained.
	if ls.LastSeen != "t3" {
		t.Errorf("ls LastSeen = %q, want t3 (most recent)", ls.LastSeen)
	}
}

func TestSortCommands_AllowCandidatesFirst(t *testing.T) {
	cmds := []command{
		{Command: "allowed", Verdict: ccsettings.ResultAllow, Outcome: outcomeRanOK},
		{Command: "prompted", Verdict: ccsettings.ResultPrompt, Outcome: outcomeRanOK},
		{Command: "denied", Verdict: ccsettings.ResultDeny, Outcome: outcomeBlocked},
		{Command: "asked", Verdict: ccsettings.ResultAsk, Outcome: outcomeRanOK},
	}
	sortCommands(cmds)
	order := []string{cmds[0].Command, cmds[1].Command, cmds[2].Command, cmds[3].Command}
	want := []string{"denied", "prompted", "asked", "allowed"}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("sorted order = %v, want %v", order, want)
			break
		}
	}
}

func TestLoadCommands_EndToEnd(t *testing.T) {
	// Build an engine that allows `ls *` but not `npm test`, so verdicts differ.
	d := buildData(t, map[string]string{
		"settings.json": `{"permissions":{"allow":["Bash(ls *)"]}}`,
	}, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "commands.jsonl")
	content := `{"phase":"pre","link_id":"a","command":"ls -la","outcome":"pending","timestamp":"t1"}
{"phase":"post","link_id":"a","command":"ls -la","outcome":"ran_ok","timestamp":"t2"}
{"phase":"pre","link_id":"b","command":"npm test","outcome":"pending","timestamp":"t3"}

not-json-garbage-line
{"phase":"pre","link_id":"c","command":"npm test","outcome":"pending","timestamp":"t4"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cmds, err := loadCommands(path, d.Engine)
	if err != nil {
		t.Fatalf("loadCommands: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands (ls deduped, npm deduped), got %d: %+v", len(cmds), cmds)
	}

	// npm test was a blocked, would-prompt command run twice → sorts first.
	if cmds[0].Command != "npm test" {
		t.Errorf("first command = %q, want npm test (highest-value candidate)", cmds[0].Command)
	}
	if cmds[0].Runs != 2 {
		t.Errorf("npm test Runs = %d, want 2", cmds[0].Runs)
	}
	if cmds[0].Outcome != outcomeBlocked {
		t.Errorf("npm test outcome = %q, want blocked", cmds[0].Outcome)
	}
	if cmds[0].Verdict != ccsettings.ResultPrompt {
		t.Errorf("npm test verdict = %v, want prompt", cmds[0].Verdict)
	}

	ls, ok := findCommand(cmds, "ls -la")
	if !ok {
		t.Fatal("ls -la not found")
	}
	if ls.Verdict != ccsettings.ResultAllow {
		t.Errorf("ls -la verdict = %v, want allow", ls.Verdict)
	}
}

func TestDiscoverJobArtifacts(t *testing.T) {
	root := t.TempDir()
	// Create .artifacts/job1/commands.jsonl and .artifacts/job2/commands.jsonl.
	for _, job := range []string{"job1", "job2"} {
		jd := filepath.Join(root, ".artifacts", job)
		if err := os.MkdirAll(jd, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jd, "commands.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A job dir without commands.jsonl must be ignored.
	if err := os.MkdirAll(filepath.Join(root, ".artifacts", "job3"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Discovery from a nested subdir must still walk up and find the artifacts.
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	jobs := discoverJobArtifacts(sub)
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d: %+v", len(jobs), jobs)
	}
	for _, j := range jobs {
		if j.PlanDir != root {
			t.Errorf("job %q PlanDir = %q, want %q", j.JobName, j.PlanDir, root)
		}
	}
}
