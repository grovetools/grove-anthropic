package ccsettings

import (
	"reflect"
	"strings"
	"testing"
)

func TestSynthesizeBashLadder(t *testing.T) {
	rungs := SynthesizeBashLadder("Bash", "npm run test src/foo.ts")
	got := make([]string, len(rungs))
	for i, r := range rungs {
		got[i] = r.Rule
	}
	want := []string{
		"Bash(npm run test src/foo.ts)",   // exact (verbatim)
		"Bash(npm run test src/foo.ts *)", // same command + any args
		"Bash(npm run test *)",            // prefix
		"Bash(npm run *)",                 // family
		"Bash(npm *)",                     // broad
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ladder rules = %v, want %v", got, want)
	}

	// Labels descend in generality, exact first.
	wantLabels := []string{"exact", "prefix", "prefix", "family", "broad"}
	for i, r := range rungs {
		if r.Label != wantLabels[i] {
			t.Errorf("rung %d label = %q, want %q", i, r.Label, wantLabels[i])
		}
	}
	// Width strictly decreases (exact pins the most tokens).
	for i := 1; i < len(rungs); i++ {
		if rungs[i].Width >= rungs[i-1].Width {
			t.Errorf("rung %d width %d not less than prev %d", i, rungs[i].Width, rungs[i-1].Width)
		}
	}
}

func TestSynthesizeBashLadderSingleToken(t *testing.T) {
	// A single-token command yields the verbatim rung plus the "+ any args" rung
	// (`ls` and `ls *`); there is no narrower prefix to widen to.
	rungs := SynthesizeBashLadder("Bash", "ls")
	got := make([]string, len(rungs))
	for i, r := range rungs {
		got[i] = r.Rule
	}
	if !reflect.DeepEqual(got, []string{"Bash(ls)", "Bash(ls *)"}) {
		t.Fatalf("single-token ladder = %v, want [Bash(ls) Bash(ls *)]", got)
	}
	if rungs[0].Label != "exact" {
		t.Errorf("first rung label = %q, want exact", rungs[0].Label)
	}
}

func TestSynthesizeBashLadderStripsWrappers(t *testing.T) {
	// A timeout wrapper is stripped before laddering so the rules match the
	// inner command (matching how the evaluator matches a wrapped subcommand).
	rungs := SynthesizeBashLadder("Bash", "timeout 30 npm run build")
	if len(rungs) == 0 {
		t.Fatal("expected rungs")
	}
	if rungs[0].Rule != "Bash(npm run build)" {
		t.Errorf("exact rung = %q, want Bash(npm run build)", rungs[0].Rule)
	}
	for _, r := range rungs {
		if r.Specifier[:3] != "npm" {
			t.Errorf("rung %q did not strip the timeout wrapper", r.Rule)
		}
	}
}

func TestSynthesizeLaddersCompound(t *testing.T) {
	// A compound command yields one ladder per subcommand.
	ladders := SynthesizeLadders("Bash", "git add . && npm run test")
	if len(ladders) != 2 {
		t.Fatalf("expected 2 ladders for compound command, got %d", len(ladders))
	}
	if ladders[0].Subcommand != "git add ." {
		t.Errorf("first subcommand = %q, want %q", ladders[0].Subcommand, "git add .")
	}
	if ladders[1].Subcommand != "npm run test" {
		t.Errorf("second subcommand = %q, want %q", ladders[1].Subcommand, "npm run test")
	}
	// Each subcommand's exact rung is its own command, not the whole compound.
	if ladders[0].Rungs[0].Rule != "Bash(git add .)" {
		t.Errorf("git ladder exact = %q", ladders[0].Rungs[0].Rule)
	}
	if ladders[1].Rungs[0].Rule != "Bash(npm run test)" {
		t.Errorf("npm ladder exact = %q", ladders[1].Rungs[0].Rule)
	}
}

func TestSynthesizedRulesParseAndMatch(t *testing.T) {
	// Every synthesized rung must parse as a Bash rule and actually match the
	// original command — the ladder is only useful if its rules cover the call.
	cmd := "npm run test src/foo.ts"
	for _, r := range SynthesizeBashLadder("Bash", cmd) {
		pr, ok := ParseRule(r.Rule)
		if !ok {
			t.Errorf("rung %q failed to parse", r.Rule)
			continue
		}
		if pr.Kind != SpecBash {
			t.Errorf("rung %q parsed as kind %d, want SpecBash", r.Rule, pr.Kind)
		}
		if !matchBashPattern(pr.Specifier, cmd) {
			t.Errorf("rung %q does not match original command %q", r.Rule, cmd)
		}
	}
}

func TestSynthesizeEmpty(t *testing.T) {
	if rungs := SynthesizeBashLadder("Bash", "   "); rungs != nil {
		t.Errorf("expected nil ladder for blank command, got %+v", rungs)
	}
}

// TestSynthesizeLaddersRealWorldCommand pins the fixes for the four synthesis
// bugs seen on a real recorded command: redirections must not leak into rules,
// 2>&1 must not produce a phantom `Bash(1)`, quoted args must not be cut, and an
// expansion subcommand must yield a note instead of un-allowlistable rungs.
func TestSynthesizeLaddersRealWorldCommand(t *testing.T) {
	cmd := `cd /tmp/x 2>/dev/null && git status 2>&1 | head -30; echo "---EXIT: $status---"`
	ladders := SynthesizeLadders("Bash", cmd)

	subs := map[string]CommandLadder{}
	for _, l := range ladders {
		subs[l.Subcommand] = l
	}

	// No rung anywhere may contain a leaked redirection or the phantom `1`.
	for _, l := range ladders {
		for _, r := range l.Rungs {
			if r.Rule == "Bash(1)" {
				t.Errorf("phantom rung %q from a split 2>&1", r.Rule)
			}
			for _, bad := range []string{"2>", "2>/dev/null"} {
				if strings.Contains(r.Specifier, bad) {
					t.Errorf("rung %q leaked redirection %q", r.Rule, bad)
				}
			}
		}
	}

	// git status got clean rungs (redirection stripped).
	gs, ok := subs["git status"]
	if !ok {
		t.Fatalf("expected a 'git status' ladder; got subs %v", keysOf(subs))
	}
	if !hasRule(gs.Rungs, "Bash(git status *)") {
		t.Errorf("git status rungs = %v, want a family rung Bash(git status *)", gs.Rungs)
	}

	// The echo subcommand contains an expansion -> note, no rungs.
	var echoLadder *CommandLadder
	for i := range ladders {
		if strings.Contains(ladders[i].Subcommand, "echo") {
			echoLadder = &ladders[i]
		}
	}
	if echoLadder == nil {
		t.Fatal("expected an echo ladder")
	}
	if len(echoLadder.Rungs) != 0 || echoLadder.Note == "" {
		t.Errorf("echo ladder = %+v, want no rungs + a note", *echoLadder)
	}
}

func TestEvaluateExpansionNotAutoApproved(t *testing.T) {
	// Even with a matching content allow rule, an expansion command prompts.
	m := mergedFromJSON(t, map[Scope]string{ScopeUser: `{"permissions":{"allow":["Bash(echo *)"]}}`})
	d := NewEngine(m, EngineOptions{}).Evaluate(ToolCall{Tool: "Bash", Command: `echo "$status"`})
	if d.Result != ResultPrompt {
		t.Errorf("Result = %v, want ResultPrompt", d.Result)
	}
	if d.Note == "" {
		t.Errorf("expected an explanatory Note about the expansion")
	}
	// A bare whole-tool allow still covers it.
	mBare := mergedFromJSON(t, map[Scope]string{ScopeUser: `{"permissions":{"allow":["Bash"]}}`})
	if d := NewEngine(mBare, EngineOptions{}).Evaluate(ToolCall{Tool: "Bash", Command: `echo "$status"`}); d.Result != ResultAllow {
		t.Errorf("bare Bash allow: Result = %v, want ResultAllow", d.Result)
	}
}

func hasRule(rungs []RuleRung, rule string) bool {
	for _, r := range rungs {
		if r.Rule == rule {
			return true
		}
	}
	return false
}
func keysOf(m map[string]CommandLadder) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}
