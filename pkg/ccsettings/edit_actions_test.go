package ccsettings

import (
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

// mergedFrom builds a one-scope MergedSettings around a raw settings document,
// the fixture PrepareEdit needs to locate the target file and re-validate. The
// typed model is parsed from a comment-stripped form (the typed parser is plain
// encoding/json), while Raw keeps the original JWCC bytes the writer reads.
func mergedFrom(t *testing.T, scope Scope, path, raw string) *MergedSettings {
	t.Helper()
	standardized, err := hujson.Standardize([]byte(raw))
	if err != nil {
		t.Fatalf("standardize fixture: %v", err)
	}
	parsed, err := Parse(standardized)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	sources := []SourceFile{{
		Scope:    scope,
		Path:     path,
		Exists:   true,
		Settings: parsed,
		Raw:      []byte(raw),
	}}
	return Merge(sources, MergeOptions{})
}

func TestActionEditsMoveRule(t *testing.T) {
	a := Action{Kind: ActionMoveRule, Rule: "Bash(rm:*)", FromTier: TierAllow, ToTier: TierDeny}
	edits, err := a.Edits()
	if err != nil {
		t.Fatalf("Edits: %v", err)
	}
	if len(edits) != 2 {
		t.Fatalf("expected 2 edits (remove+append), got %d", len(edits))
	}
	if edits[0].Op != OpArrayRemove || strings.Join(edits[0].Path, ".") != "permissions.allow" {
		t.Errorf("first edit should remove from permissions.allow, got %+v", edits[0])
	}
	if edits[1].Op != OpArrayAppend || strings.Join(edits[1].Path, ".") != "permissions.deny" {
		t.Errorf("second edit should append to permissions.deny, got %+v", edits[1])
	}
}

func TestActionEditsMoveRuleSameTierRejected(t *testing.T) {
	a := Action{Kind: ActionMoveRule, Rule: "Read", FromTier: TierAllow, ToTier: TierAllow}
	if _, err := a.Edits(); err == nil {
		t.Fatal("moving a rule to the same tier should error")
	}
}

func TestActionEditsDomainListValidated(t *testing.T) {
	a := Action{Kind: ActionAddDomain, Value: "example.com", DomainList: "bogusList"}
	if _, err := a.Edits(); err == nil {
		t.Fatal("an invalid domain list should be rejected")
	}
	ok := Action{Kind: ActionAddDomain, Value: "example.com", DomainList: "allowedDomains"}
	edits, err := ok.Edits()
	if err != nil {
		t.Fatalf("valid domain add: %v", err)
	}
	if edits[0].Op != OpArrayAppend || strings.Join(edits[0].Path, ".") != "sandbox.network.allowedDomains" {
		t.Errorf("domain edit path wrong: %+v", edits[0])
	}
}

// PrepareEdit must reject the Managed scope with ErrManagedScope, never
// producing a plan that could be committed.
func TestPrepareEditRejectsManagedScope(t *testing.T) {
	merged := mergedFrom(t, ScopeManaged, "/policy/managed-settings.json",
		`{"permissions":{"allow":["Read"]}}`)
	_, err := PrepareEdit(merged, ScopeManaged,
		Action{Kind: ActionMoveRule, Rule: "Read", FromTier: TierAllow, ToTier: TierDeny})
	if err != ErrManagedScope {
		t.Fatalf("expected ErrManagedScope, got %v", err)
	}
}

func TestPrepareEditRejectsCLIScope(t *testing.T) {
	merged := mergedFrom(t, ScopeUser, "/home/u/.claude/settings.json", `{}`)
	if _, err := PrepareEdit(merged, ScopeCLI,
		Action{Kind: ActionAddDirectory, Value: "/tmp"}); err == nil {
		t.Fatal("the transient CLI scope has no file and must be rejected")
	}
}

// The crux at the action layer: preparing an edit against a comment-bearing
// settings file produces an After rendering that still carries the comment and
// the untouched sibling keys — proof the writer path is wired through.
func TestPrepareEditPreservesCommentsAndSiblings(t *testing.T) {
	raw := `{
  // hand-authored note
  "futureKey": "keep-me",
  "permissions": {
    "allow": ["Read"],
    "deny": []
  }
}`
	merged := mergedFrom(t, ScopeUser, "/home/u/.claude/settings.json", raw)
	plan, err := PrepareEdit(merged, ScopeUser,
		Action{Kind: ActionMoveRule, Rule: "Read", FromTier: TierAllow, ToTier: TierDeny})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	after := string(plan.After)
	if !strings.Contains(after, "// hand-authored note") {
		t.Errorf("comment was stripped from prepared edit:\n%s", after)
	}
	if !strings.Contains(after, `"futureKey": "keep-me"`) {
		t.Errorf("unknown sibling key dropped:\n%s", after)
	}
	// Read moved out of allow into deny.
	allowIdx := strings.Index(after, `"allow"`)
	denyIdx := strings.Index(after, `"deny"`)
	readIdx := strings.LastIndex(after, `"Read"`)
	if allowIdx < 0 || denyIdx < 0 || readIdx < 0 {
		t.Fatalf("expected allow, deny, and Read in output:\n%s", after)
	}
	if readIdx < denyIdx {
		t.Errorf("Read should now live under deny, not allow:\n%s", after)
	}
	if plan.Path != "/home/u/.claude/settings.json" {
		t.Errorf("plan path wrong: %s", plan.Path)
	}
	if plan.Created {
		t.Error("editing an existing file should not be marked Created")
	}
}

func TestPrepareEditCreatesAbsentFile(t *testing.T) {
	// A scope whose file does not exist: PrepareEdit should still render an
	// After from an empty document and mark the plan Created.
	parsed, _ := Parse(nil)
	sources := []SourceFile{{
		Scope:    ScopeUser,
		Path:     "/home/u/.claude/settings.json",
		Exists:   false,
		Settings: parsed,
	}}
	merged := Merge(sources, MergeOptions{})
	plan, err := PrepareEdit(merged, ScopeUser,
		Action{Kind: ActionAddDirectory, Value: "/srv"})
	if err != nil {
		t.Fatalf("PrepareEdit on absent file: %v", err)
	}
	if !plan.Created {
		t.Error("absent file should be marked Created")
	}
	if !strings.Contains(string(plan.After), "/srv") {
		t.Errorf("new directory missing from output:\n%s", plan.After)
	}
}
