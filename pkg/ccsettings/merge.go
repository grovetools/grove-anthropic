package ccsettings

// This file implements Claude Code's settings merge EXPLICITLY rather than
// reusing grove's generic LoadLayered, because Claude's semantics diverge in
// two load-bearing ways:
//
//  1. The top layer is inverted. Managed is a ceiling (highest precedence,
//     cannot be overridden by CLI args or any file), and a transient CLI-args
//     layer sits above Local. grove instead treats its Global layer as the
//     lowest-precedence base.
//
//  2. Permission and sandbox arrays merge ADDITIVELY across scopes (not
//     scalar last-write-wins), and managed lockdown flags SUBTRACT lower
//     scopes. Precedence picks the winner only for scalars.

// ProvenancedRule is a permission rule string tagged with the scope it came from.
type ProvenancedRule struct {
	Rule  string
	Scope Scope
}

// ProvenancedString is an array element (a path or domain) tagged with its scope.
type ProvenancedString struct {
	Value string
	Scope Scope
}

// ProvenancedBool is a resolved scalar boolean tagged with the deciding scope.
// Set is false when no scope defined the key.
type ProvenancedBool struct {
	Set   bool
	Value bool
	Scope Scope
}

// ResolveContext supplies the path roots used when resolving anchored Read/Edit
// rules and sandbox filesystem paths.
type ResolveContext struct {
	CWD         string
	ProjectRoot string
	HomeDir     string
}

// MergeOptions parameterizes Merge.
type MergeOptions struct {
	// CLI, when non-nil, injects the transient CLI-args layer (--add-dir,
	// --allowedTools) at its precedence rung between Managed and Local.
	CLI *Settings
	// Context supplies path roots for later anchor resolution.
	Context ResolveContext
}

// MergedSettings is the cross-scope resolved view with per-key/per-element
// provenance. Arrays carry one entry per contributing scope (highest
// precedence first); scalars carry the single deciding scope.
type MergedSettings struct {
	Sources []SourceFile
	Context ResolveContext

	// Permission rule arrays (additive across scopes).
	Allow                 []ProvenancedRule
	Ask                   []ProvenancedRule
	Deny                  []ProvenancedRule
	AdditionalDirectories []ProvenancedString

	// Permission scalars (precedence wins).
	DefaultMode                  ProvenancedString
	DisableBypassPermissionsMode ProvenancedString
	DisableAutoMode              ProvenancedString

	// SkipDangerousModePermissionPrompt is a top-level permission-adjacent
	// toggle (bypass-mode dialog acceptance); precedence wins.
	SkipDangerousModePermissionPrompt ProvenancedBool

	// Managed lockdowns (resolved from the managed scope only).
	AllowManagedPermissionRulesOnly bool
	AllowManagedReadPathsOnly       bool
	AllowManagedDomainsOnly         bool

	// Sandbox scalars.
	SandboxEnabled                  ProvenancedBool
	SandboxFailIfUnavailable        ProvenancedBool
	SandboxAllowUnsandboxedCommands ProvenancedBool
	// SandboxAutoAllowBashIfSandboxed is the auto-allow mode toggle. Defaults to
	// true when the sandbox is enabled; see EffectiveAutoAllowBash.
	SandboxAutoAllowBashIfSandboxed ProvenancedBool
	SandboxExcludedCommands         []ProvenancedString

	// Sandbox filesystem arrays (raw; ComputeFilesystemBoundary folds in
	// Read/Edit deny rules and additionalDirectories).
	FSAllowWrite []ProvenancedString
	FSDenyWrite  []ProvenancedString
	FSAllowRead  []ProvenancedString
	FSDenyRead   []ProvenancedString

	// Sandbox network arrays (raw; ComputeNetworkPolicy folds in WebFetch rules).
	NetAllowedDomains []ProvenancedString
	NetDeniedDomains  []ProvenancedString
}

// scopeSettings pairs a scope with its parsed settings for ordered iteration.
type scopeSettings struct {
	scope Scope
	s     *Settings
}

// Merge resolves discovered sources (plus an optional CLI layer) into a
// MergedSettings under Claude's precedence Managed > CLI > Local > Project > User.
func Merge(sources []SourceFile, opts MergeOptions) *MergedSettings {
	// Index parsed settings by scope.
	byScope := map[Scope]*Settings{}
	for _, sf := range sources {
		if sf.Settings != nil {
			byScope[sf.Scope] = sf.Settings
		}
	}
	if opts.CLI != nil {
		byScope[ScopeCLI] = opts.CLI
	}

	// highToLow drives additive array merges (winning rule reported first);
	// lowToHigh drives scalar last-write-wins.
	highToLow := orderedScopes([]Scope{ScopeManaged, ScopeCLI, ScopeLocal, ScopeProject, ScopeUser}, byScope)
	lowToHigh := orderedScopes([]Scope{ScopeUser, ScopeProject, ScopeLocal, ScopeCLI, ScopeManaged}, byScope)

	m := &MergedSettings{Sources: sources, Context: opts.Context}

	// Resolve managed lockdowns up front; they gate the array merges below.
	if mgd := byScope[ScopeManaged]; mgd != nil {
		if mgd.Permissions != nil && boolVal(mgd.Permissions.AllowManagedPermissionRulesOnly) {
			m.AllowManagedPermissionRulesOnly = true
		}
		if fs := managedFilesystem(mgd); fs != nil && boolVal(fs.AllowManagedReadPathsOnly) {
			m.AllowManagedReadPathsOnly = true
		}
		if net := managedNetwork(mgd); net != nil && boolVal(net.AllowManagedDomainsOnly) {
			m.AllowManagedDomainsOnly = true
		}
	}

	// --- Permission rule arrays (additive) ---
	for _, ss := range highToLow {
		if ss.s.Permissions == nil {
			continue
		}
		p := ss.s.Permissions
		// Managed-permission-rules-only subtracts every non-managed scope.
		if m.AllowManagedPermissionRulesOnly && ss.scope != ScopeManaged {
			continue
		}
		m.Allow = appendRules(m.Allow, p.Allow, ss.scope)
		m.Ask = appendRules(m.Ask, p.Ask, ss.scope)
		m.Deny = appendRules(m.Deny, p.Deny, ss.scope)
		m.AdditionalDirectories = appendStrings(m.AdditionalDirectories, p.AdditionalDirectories, ss.scope)
	}

	// --- Permission scalars (precedence) ---
	for _, ss := range lowToHigh {
		// SkipDangerousModePermissionPrompt is top-level, not under permissions.
		if ss.s.SkipDangerousModePermissionPrompt != nil {
			m.SkipDangerousModePermissionPrompt = ProvenancedBool{
				Set: true, Value: *ss.s.SkipDangerousModePermissionPrompt, Scope: ss.scope,
			}
		}
		if ss.s.Permissions == nil {
			continue
		}
		p := ss.s.Permissions
		if p.DefaultMode != "" {
			m.DefaultMode = ProvenancedString{Value: p.DefaultMode, Scope: ss.scope}
		}
		if p.DisableBypassPermissionsMode != "" {
			m.DisableBypassPermissionsMode = ProvenancedString{Value: p.DisableBypassPermissionsMode, Scope: ss.scope}
		}
		if p.DisableAutoMode != "" {
			m.DisableAutoMode = ProvenancedString{Value: p.DisableAutoMode, Scope: ss.scope}
		}
	}

	// --- Sandbox scalars (precedence) ---
	for _, ss := range lowToHigh {
		sb := ss.s.Sandbox
		if sb == nil {
			continue
		}
		if sb.Enabled != nil {
			m.SandboxEnabled = ProvenancedBool{Set: true, Value: *sb.Enabled, Scope: ss.scope}
		}
		if sb.FailIfUnavailable != nil {
			m.SandboxFailIfUnavailable = ProvenancedBool{Set: true, Value: *sb.FailIfUnavailable, Scope: ss.scope}
		}
		if sb.AllowUnsandboxedCommands != nil {
			m.SandboxAllowUnsandboxedCommands = ProvenancedBool{Set: true, Value: *sb.AllowUnsandboxedCommands, Scope: ss.scope}
		}
		if sb.AutoAllowBashIfSandboxed != nil {
			m.SandboxAutoAllowBashIfSandboxed = ProvenancedBool{Set: true, Value: *sb.AutoAllowBashIfSandboxed, Scope: ss.scope}
		}
	}

	// --- Sandbox arrays (additive) ---
	for _, ss := range highToLow {
		sb := ss.s.Sandbox
		if sb == nil {
			continue
		}
		m.SandboxExcludedCommands = appendStrings(m.SandboxExcludedCommands, sb.ExcludedCommands, ss.scope)
		if fs := sb.Filesystem; fs != nil {
			m.FSDenyWrite = appendStrings(m.FSDenyWrite, fs.DenyWrite, ss.scope)
			m.FSDenyRead = appendStrings(m.FSDenyRead, fs.DenyRead, ss.scope)
			m.FSAllowWrite = appendStrings(m.FSAllowWrite, fs.AllowWrite, ss.scope)
			// allowManagedReadPathsOnly: only managed allowRead survives.
			if !m.AllowManagedReadPathsOnly || ss.scope == ScopeManaged {
				m.FSAllowRead = appendStrings(m.FSAllowRead, fs.AllowRead, ss.scope)
			}
		}
		if net := sb.Network; net != nil {
			m.NetDeniedDomains = appendStrings(m.NetDeniedDomains, net.DeniedDomains, ss.scope)
			// allowManagedDomainsOnly: only managed allowedDomains survive.
			if !m.AllowManagedDomainsOnly || ss.scope == ScopeManaged {
				m.NetAllowedDomains = appendStrings(m.NetAllowedDomains, net.AllowedDomains, ss.scope)
			}
		}
	}

	return m
}

// orderedScopes filters the given scope order down to scopes that have settings.
func orderedScopes(order []Scope, byScope map[Scope]*Settings) []scopeSettings {
	var out []scopeSettings
	for _, sc := range order {
		if s := byScope[sc]; s != nil {
			out = append(out, scopeSettings{scope: sc, s: s})
		}
	}
	return out
}

func appendRules(dst []ProvenancedRule, rules []string, scope Scope) []ProvenancedRule {
	for _, r := range rules {
		dst = append(dst, ProvenancedRule{Rule: r, Scope: scope})
	}
	return dst
}

func appendStrings(dst []ProvenancedString, vals []string, scope Scope) []ProvenancedString {
	for _, v := range vals {
		dst = append(dst, ProvenancedString{Value: v, Scope: scope})
	}
	return dst
}

func boolVal(b *bool) bool { return b != nil && *b }

func managedFilesystem(s *Settings) *SandboxFilesystem {
	if s.Sandbox == nil {
		return nil
	}
	return s.Sandbox.Filesystem
}

func managedNetwork(s *Settings) *SandboxNetwork {
	if s.Sandbox == nil {
		return nil
	}
	return s.Sandbox.Network
}
