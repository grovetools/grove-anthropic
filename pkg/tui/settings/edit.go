package settings

import (
	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// This file defines the TUI-side edit vocabulary: the per-row payloads pages
// attach to selectable leaves, the intent a row raises when the user presses
// enter/space on it, and the messages the overlay exchanges with the Model. The
// heavy lifting (lowering an intent to writer edits, re-validating, writing)
// lives in pkg/ccsettings (edit_actions.go + writer.go); this layer is purely
// about turning a cursor position into an ccsettings.Action and a target scope.

// editKind classifies what a selectable row edits, so the overlay knows whether
// it needs a text field (add) and which ccsettings.Action to build on confirm.
type editKind int

const (
	// editToggleRule cycles a rule allow → ask → deny within one scope.
	editToggleRule editKind = iota
	// editRemoveRule deletes a rule from its tier.
	editRemoveRule
	// editAddDirectory / editRemoveDirectory edit additionalDirectories.
	editAddDirectory
	editRemoveDirectory
	// editToggleSandboxBool flips a sandbox boolean.
	editToggleSandboxBool
	// editAddDomain / editRemoveDomain edit a sandbox.network domain list.
	editAddDomain
	editRemoveDomain
)

// rulePayload is attached to a permission-rule leaf. It carries everything the
// overlay needs to move or remove the rule.
type rulePayload struct {
	rule  string
	tier  ccsettings.RuleTier
	scope ccsettings.Scope
}

// dirPayload is attached to an additionalDirectories leaf, or to the
// "add directory" affordance (value empty → prompts for input).
type dirPayload struct {
	value  string
	scope  ccsettings.Scope
	remove bool // true on an existing entry (remove), false on the add affordance
}

// sandboxBoolPayload is attached to a sandbox boolean row.
type sandboxBoolPayload struct {
	key     string // "enabled", "failIfUnavailable", "allowUnsandboxedCommands"
	current bool
	set     bool // whether any scope currently sets it
	scope   ccsettings.Scope
}

// domainPayload is attached to a network-domain leaf, or to the "add domain"
// affordance.
type domainPayload struct {
	value  string
	list   string // "allowedDomains" or "deniedDomains"
	scope  ccsettings.Scope
	remove bool
}

// editIntent is the page-agnostic description of an edit a row wants to make.
// The active page builds it from the selected leaf's payload and emits an
// editRequestMsg; the Model opens the overlay on it.
type editIntent struct {
	kind  editKind
	title string // human label for the overlay header

	// needsInput is true when the user must type a value (add directory / domain
	// / rule). The overlay shows a text field seeded with seed.
	needsInput bool
	seed       string

	// suggestedScope is the scope the edit defaults to (the row's own scope for
	// edits-in-place; the highest writable scope for adds).
	suggestedScope ccsettings.Scope

	// readOnly marks an intent the guardrails forbid (managed lockdown active,
	// or a managed-scope row). The overlay shows the reason and offers no write.
	readOnly bool
	reason   string

	// build turns a chosen scope + entered value into the ccsettings.Action to
	// prepare. value is "" for non-input edits.
	build func(scope ccsettings.Scope, value string) ccsettings.Action
}

// editRequestMsg is emitted by a page when the user actions a selectable row.
type editRequestMsg struct{ intent editIntent }

// editCommittedMsg is emitted by the overlay after a successful write so the
// Model can reload Data and rebuild pages.
type editCommittedMsg struct{ path string }

// writableScopes lists the scopes a user may write, in the overlay's
// tab-cycle order (the same personal→committed→local reading order the rest of
// the UI uses, minus the read-only Managed ceiling and the transient CLI rung).
var writableScopes = []ccsettings.Scope{
	ccsettings.ScopeUser,
	ccsettings.ScopeProject,
	ccsettings.ScopeLocal,
}

// isWritableScope reports whether a scope can be targeted by an edit.
func isWritableScope(s ccsettings.Scope) bool {
	for _, w := range writableScopes {
		if w == s {
			return true
		}
	}
	return false
}

// defaultTargetScope picks the scope an edit defaults to: the row's own scope
// when it is writable, else the first writable scope (User).
func defaultTargetScope(rowScope ccsettings.Scope) ccsettings.Scope {
	if isWritableScope(rowScope) {
		return rowScope
	}
	return writableScopes[0]
}
