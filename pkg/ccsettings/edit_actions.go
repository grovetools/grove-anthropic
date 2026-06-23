package ccsettings

import (
	"fmt"

	"github.com/tailscale/hujson"
)

// This file is the bridge between the settings TUI's high-level intents
// (toggle a rule's tier, add a directory, flip a sandbox flag, edit a domain
// list) and the low-level comment-preserving writer (writer.go). It produces
// the []Edit a write needs AND re-validates the candidate document by
// re-parsing + re-merging + re-evaluating, so a malformed or self-contradictory
// edit is caught before anything touches disk.

// RuleTier names the permission bucket a rule lives in.
type RuleTier int

const (
	TierAllow RuleTier = iota
	TierAsk
	TierDeny
)

func (t RuleTier) String() string {
	switch t {
	case TierAllow:
		return "allow"
	case TierAsk:
		return "ask"
	case TierDeny:
		return "deny"
	default:
		return fmt.Sprintf("tier(%d)", int(t))
	}
}

// tierKey maps a tier to its permissions array key.
func tierKey(t RuleTier) string {
	switch t {
	case TierAsk:
		return "ask"
	case TierDeny:
		return "deny"
	default:
		return "allow"
	}
}

// NextTier cycles allow -> ask -> deny -> allow, the order the TUI steps a rule
// through on each toggle.
func NextTier(t RuleTier) RuleTier {
	switch t {
	case TierAllow:
		return TierAsk
	case TierAsk:
		return TierDeny
	default:
		return TierAllow
	}
}

// ActionKind enumerates the high-level edit intents the TUI exposes.
type ActionKind int

const (
	// ActionMoveRule moves a permission rule from one tier to another within a
	// single scope file (e.g. allow -> ask). Used by the allow<->ask<->deny
	// toggle.
	ActionMoveRule ActionKind = iota
	// ActionAddRule adds a rule string to a tier.
	ActionAddRule
	// ActionRemoveRule removes a rule string from a tier.
	ActionRemoveRule
	// ActionAddDirectory appends to permissions.additionalDirectories.
	ActionAddDirectory
	// ActionRemoveDirectory removes from permissions.additionalDirectories.
	ActionRemoveDirectory
	// ActionSetSandboxBool sets a boolean under the sandbox object.
	ActionSetSandboxBool
	// ActionAddDomain appends to a sandbox.network domain list.
	ActionAddDomain
	// ActionRemoveDomain removes from a sandbox.network domain list.
	ActionRemoveDomain
)

// Action is a high-level edit the TUI requests against a chosen scope file.
// Only the fields relevant to Kind need be set.
type Action struct {
	Kind ActionKind

	// Rule moves/adds/removes.
	Rule     string
	FromTier RuleTier
	ToTier   RuleTier

	// Directory / domain value.
	Value string
	// DomainList is "allowedDomains" or "deniedDomains" for domain actions.
	DomainList string

	// Sandbox boolean key (under "sandbox", e.g. "enabled") and value.
	SandboxKey string
	BoolVal    bool
}

// Edits lowers an Action into the writer's []Edit. The edits target the chosen
// scope's settings.json by path; scope selection is the caller's concern.
func (a Action) Edits() ([]Edit, error) {
	switch a.Kind {
	case ActionMoveRule:
		if a.Rule == "" {
			return nil, fmt.Errorf("move rule: empty rule")
		}
		if a.FromTier == a.ToTier {
			return nil, fmt.Errorf("move rule: source and target tier are both %s", a.FromTier)
		}
		return []Edit{
			{Op: OpArrayRemove, Path: []string{"permissions", tierKey(a.FromTier)}, StringVal: a.Rule},
			{Op: OpArrayAppend, Path: []string{"permissions", tierKey(a.ToTier)}, StringVal: a.Rule},
		}, nil
	case ActionAddRule:
		if a.Rule == "" {
			return nil, fmt.Errorf("add rule: empty rule")
		}
		return []Edit{
			{Op: OpArrayAppend, Path: []string{"permissions", tierKey(a.ToTier)}, StringVal: a.Rule},
		}, nil
	case ActionRemoveRule:
		if a.Rule == "" {
			return nil, fmt.Errorf("remove rule: empty rule")
		}
		return []Edit{
			{Op: OpArrayRemove, Path: []string{"permissions", tierKey(a.FromTier)}, StringVal: a.Rule},
		}, nil
	case ActionAddDirectory:
		if a.Value == "" {
			return nil, fmt.Errorf("add directory: empty path")
		}
		return []Edit{
			{Op: OpArrayAppend, Path: []string{"permissions", "additionalDirectories"}, StringVal: a.Value},
		}, nil
	case ActionRemoveDirectory:
		if a.Value == "" {
			return nil, fmt.Errorf("remove directory: empty path")
		}
		return []Edit{
			{Op: OpArrayRemove, Path: []string{"permissions", "additionalDirectories"}, StringVal: a.Value},
		}, nil
	case ActionSetSandboxBool:
		if a.SandboxKey == "" {
			return nil, fmt.Errorf("set sandbox bool: empty key")
		}
		return []Edit{
			{Op: OpSet, Path: []string{"sandbox", a.SandboxKey}, ValueKind: KindBool, BoolVal: a.BoolVal},
		}, nil
	case ActionAddDomain:
		if err := validDomainList(a.DomainList); err != nil {
			return nil, err
		}
		if a.Value == "" {
			return nil, fmt.Errorf("add domain: empty domain")
		}
		return []Edit{
			{Op: OpArrayAppend, Path: []string{"sandbox", "network", a.DomainList}, StringVal: a.Value},
		}, nil
	case ActionRemoveDomain:
		if err := validDomainList(a.DomainList); err != nil {
			return nil, err
		}
		if a.Value == "" {
			return nil, fmt.Errorf("remove domain: empty domain")
		}
		return []Edit{
			{Op: OpArrayRemove, Path: []string{"sandbox", "network", a.DomainList}, StringVal: a.Value},
		}, nil
	default:
		return nil, fmt.Errorf("unknown action kind %d", int(a.Kind))
	}
}

func validDomainList(list string) error {
	if list != "allowedDomains" && list != "deniedDomains" {
		return fmt.Errorf("domain list must be allowedDomains or deniedDomains, got %q", list)
	}
	return nil
}

// Describe renders a one-line human summary for the dry-run preview header.
func (a Action) Describe() string {
	switch a.Kind {
	case ActionMoveRule:
		return fmt.Sprintf("move %q: %s → %s", a.Rule, a.FromTier, a.ToTier)
	case ActionAddRule:
		return fmt.Sprintf("add %q to %s", a.Rule, a.ToTier)
	case ActionRemoveRule:
		return fmt.Sprintf("remove %q from %s", a.Rule, a.FromTier)
	case ActionAddDirectory:
		return fmt.Sprintf("add directory %q", a.Value)
	case ActionRemoveDirectory:
		return fmt.Sprintf("remove directory %q", a.Value)
	case ActionSetSandboxBool:
		return fmt.Sprintf("set sandbox.%s = %t", a.SandboxKey, a.BoolVal)
	case ActionAddDomain:
		return fmt.Sprintf("add %q to sandbox.network.%s", a.Value, a.DomainList)
	case ActionRemoveDomain:
		return fmt.Sprintf("remove %q from sandbox.network.%s", a.Value, a.DomainList)
	default:
		return "edit"
	}
}

// EditPlan is the result of preparing an Action against a target scope: the
// edits to apply, the rendered before/after JSON for a dry-run, and the path
// that would be written.
type EditPlan struct {
	Scope   Scope
	Path    string
	Action  Action
	Edits   []Edit
	Before  []byte
	After   []byte
	Created bool // true when the target file does not yet exist
}

// ErrManagedScope is returned when a write targets the managed scope, which is
// policy-owned and never writable from the TUI.
var ErrManagedScope = fmt.Errorf("the Managed scope is read-only and cannot be edited")

// PrepareEdit validates an Action against a target scope and returns an
// EditPlan with before/after renderings for a dry-run. It does NOT write.
//
// Guardrails enforced here:
//   - the Managed scope is never writable (ErrManagedScope);
//   - the candidate document is re-parsed and re-validated through the engine
//     so a self-contradictory or malformed edit is rejected before disk;
//   - unknown keys are preserved by construction (the writer mutates only the
//     targeted path).
func PrepareEdit(merged *MergedSettings, scope Scope, action Action) (*EditPlan, error) {
	if scope == ScopeManaged {
		return nil, ErrManagedScope
	}
	if scope == ScopeCLI {
		return nil, fmt.Errorf("the CLI scope is transient and has no settings file")
	}

	path, sf := sourceForScope(merged, scope)
	if path == "" {
		return nil, fmt.Errorf("no settings path for scope %s", scope)
	}

	edits, err := action.Edits()
	if err != nil {
		return nil, err
	}

	before := []byte(nil)
	created := true
	if sf != nil && sf.Exists {
		before = sf.Raw
		created = false
	}

	after, err := ApplyEdits(before, edits)
	if err != nil {
		return nil, fmt.Errorf("apply edits: %w", err)
	}

	// Re-validate: the edited document must still parse, and the whole merged
	// view it produces must still build an engine without error. The typed
	// parser is plain encoding/json, so comments/trailing commas in the JWCC
	// output are standardized away first. hujson.Standardize mutates its input
	// in place, so a copy is standardized — the on-disk After bytes keep their
	// comments and trailing commas intact.
	standardized, err := hujson.Standardize(append([]byte(nil), after...))
	if err != nil {
		return nil, fmt.Errorf("edited settings would not parse: %w", err)
	}
	if _, err := Parse(standardized); err != nil {
		return nil, fmt.Errorf("edited settings would not parse: %w", err)
	}
	if err := revalidate(merged, scope, standardized); err != nil {
		return nil, fmt.Errorf("edited settings failed validation: %w", err)
	}

	return &EditPlan{
		Scope:   scope,
		Path:    path,
		Action:  action,
		Edits:   edits,
		Before:  before,
		After:   after,
		Created: created,
	}, nil
}

// Commit writes a prepared plan to disk (atomic temp+rename). Re-checks the
// managed guardrail defensively.
func (p *EditPlan) Commit() error {
	if p.Scope == ScopeManaged {
		return ErrManagedScope
	}
	return WriteEdits(p.Path, p.Edits)
}

// sourceForScope returns the on-disk path and (if present) the SourceFile for a
// scope within the merged view.
func sourceForScope(merged *MergedSettings, scope Scope) (string, *SourceFile) {
	for i := range merged.Sources {
		if merged.Sources[i].Scope == scope {
			return merged.Sources[i].Path, &merged.Sources[i]
		}
	}
	return "", nil
}

// revalidate rebuilds the merged view with the candidate scope bytes swapped in
// and constructs an engine, surfacing any structural error the edit introduces.
func revalidate(merged *MergedSettings, scope Scope, candidate []byte) error {
	cand, err := Parse(candidate)
	if err != nil {
		return err
	}
	sources := make([]SourceFile, len(merged.Sources))
	copy(sources, merged.Sources)
	found := false
	for i := range sources {
		if sources[i].Scope == scope {
			sources[i].Settings = cand
			sources[i].Exists = true
			sources[i].Raw = candidate
			sources[i].ParseError = nil
			found = true
			break
		}
	}
	if !found {
		sources = append(sources, SourceFile{Scope: scope, Settings: cand, Exists: true, Raw: candidate})
	}
	re := Merge(sources, MergeOptions{Context: merged.Context})
	// Building the engine parses every rule; a structural problem surfaces here.
	_ = NewEngine(re, EngineOptions{})
	return nil
}
