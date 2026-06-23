package ccsettings

import (
	"reflect"
	"testing"
)

func TestSynthesizeBashLadder(t *testing.T) {
	rungs := SynthesizeBashLadder("Bash", "npm run test src/foo.ts")
	got := make([]string, len(rungs))
	for i, r := range rungs {
		got[i] = r.Rule
	}
	want := []string{
		"Bash(npm run test src/foo.ts)", // exact
		"Bash(npm run test *)",          // prefix
		"Bash(npm run *)",               // family
		"Bash(npm *)",                   // broad
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ladder rules = %v, want %v", got, want)
	}

	// Labels descend in generality, exact first.
	wantLabels := []string{"exact", "prefix", "family", "broad"}
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
	// A single-token command has only the exact rung — there is no prefix to
	// widen to (the broad rung would equal the exact one and is deduped).
	rungs := SynthesizeBashLadder("Bash", "ls")
	if len(rungs) != 1 {
		t.Fatalf("expected 1 rung for single-token command, got %d: %+v", len(rungs), rungs)
	}
	if rungs[0].Rule != "Bash(ls)" || rungs[0].Label != "exact" {
		t.Errorf("unexpected rung %+v", rungs[0])
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
