package ccsettings

import "strings"

// BoundaryEntry is one resolved path or domain contributing to the effective
// sandbox boundary, tagged with where it came from.
type BoundaryEntry struct {
	Path   string // resolved absolute path/glob (filesystem) or domain (network)
	Raw    string // original specifier as written
	Scope  Scope
	Source string // e.g. "sandbox.filesystem.allowWrite", "permissions.deny:Read"
}

// FilesystemBoundary is the effective read/write boundary, merging
// sandbox.filesystem.* with Read/Edit permission rules and additionalDirectories.
type FilesystemBoundary struct {
	// SandboxEnabled reflects whether the OS sandbox is on. When false, the
	// boundary is not OS-enforced — the entries describe the no-prompt scope
	// rather than a hard limit, and the read default is the working dir instead
	// of the entire filesystem.
	SandboxEnabled bool
	AllowWrite     []BoundaryEntry
	DenyWrite      []BoundaryEntry
	AllowRead      []BoundaryEntry
	DenyRead       []BoundaryEntry
}

// ComputeFilesystemBoundary folds the sandbox.filesystem arrays, the Edit allow
// rules (which grant write access the same way allowWrite does), the Read/Edit
// deny rules (which block access), and additionalDirectories (readable +
// writable) into a single effective boundary.
//
// The default writable working directory and the default whole-computer read
// access are represented as implicit entries so the boundary is self-describing.
func ComputeFilesystemBoundary(m *MergedSettings) FilesystemBoundary {
	ctx := m.Context
	var fb FilesystemBoundary

	// Implicit defaults depend on whether the OS sandbox is enabled.
	//
	//   - Write: the working directory (and, under the sandbox, the session temp
	//     dir) is writable. Without the sandbox there is no OS write boundary;
	//     the working dir is what Claude edits without prompting.
	//   - Read: under the sandbox the default is the ENTIRE computer minus
	//     denyRead — which still exposes credential files like ~/.ssh and
	//     ~/.aws/credentials unless they are denied. Without the sandbox there is
	//     no OS read boundary; the working dir is what Claude reads without
	//     prompting.
	sandboxOn := m.SandboxEnabled.Value
	fb.SandboxEnabled = sandboxOn
	if ctx.CWD != "" {
		cwd := absCandidate(".", ctx)
		fb.AllowWrite = append(fb.AllowWrite, BoundaryEntry{Path: cwd, Raw: ".", Source: "default:workingDirectory"})
		if !sandboxOn {
			fb.AllowRead = append(fb.AllowRead, BoundaryEntry{Path: cwd, Raw: ".", Source: "default:workingDirectory"})
		}
	}
	if sandboxOn {
		fb.AllowRead = append(fb.AllowRead, BoundaryEntry{
			Path: "/", Raw: "/", Source: "default:sandbox (entire filesystem, minus denyRead)",
		})
		fb.AllowWrite = append(fb.AllowWrite, BoundaryEntry{
			Path: "$TMPDIR", Raw: "", Source: "default:sandbox (session temp dir)",
		})
	}

	// sandbox.filesystem arrays.
	for _, e := range m.FSAllowWrite {
		fb.AllowWrite = append(fb.AllowWrite, sandboxFSEntry(e, "sandbox.filesystem.allowWrite", ctx))
	}
	for _, e := range m.FSDenyWrite {
		fb.DenyWrite = append(fb.DenyWrite, sandboxFSEntry(e, "sandbox.filesystem.denyWrite", ctx))
	}
	for _, e := range m.FSAllowRead {
		fb.AllowRead = append(fb.AllowRead, sandboxFSEntry(e, "sandbox.filesystem.allowRead", ctx))
	}
	for _, e := range m.FSDenyRead {
		fb.DenyRead = append(fb.DenyRead, sandboxFSEntry(e, "sandbox.filesystem.denyRead", ctx))
	}

	// Edit allow rules grant write; Read/Edit deny rules block.
	for _, r := range m.Allow {
		if pr, ok := ParseRule(r.Rule); ok && pr.Tool == "Edit" && pr.Kind == SpecPath {
			fb.AllowWrite = append(fb.AllowWrite, BoundaryEntry{
				Path: resolveReadEditAnchor(pr.Specifier, ctx), Raw: r.Rule, Scope: r.Scope,
				Source: "permissions.allow:Edit",
			})
		}
	}
	for _, r := range m.Deny {
		pr, ok := ParseRule(r.Rule)
		if !ok || pr.Kind != SpecPath {
			continue
		}
		glob := resolveReadEditAnchor(pr.Specifier, ctx)
		switch pr.Tool {
		case "Read":
			fb.DenyRead = append(fb.DenyRead, BoundaryEntry{Path: glob, Raw: r.Rule, Scope: r.Scope, Source: "permissions.deny:Read"})
		case "Edit", "Write", "MultiEdit":
			fb.DenyWrite = append(fb.DenyWrite, BoundaryEntry{Path: glob, Raw: r.Rule, Scope: r.Scope, Source: "permissions.deny:Edit"})
		}
	}

	// additionalDirectories become readable and writable.
	for _, d := range m.AdditionalDirectories {
		abs := absCandidate(d.Value, ctx)
		fb.AllowRead = append(fb.AllowRead, BoundaryEntry{Path: abs, Raw: d.Value, Scope: d.Scope, Source: "additionalDirectories"})
		fb.AllowWrite = append(fb.AllowWrite, BoundaryEntry{Path: abs, Raw: d.Value, Scope: d.Scope, Source: "additionalDirectories"})
	}

	return fb
}

func sandboxFSEntry(e ProvenancedString, source string, ctx ResolveContext) BoundaryEntry {
	return BoundaryEntry{
		Path:   resolveSandboxPath(e.Value, e.Scope, ctx),
		Raw:    e.Value,
		Scope:  e.Scope,
		Source: source,
	}
}

// resolveSandboxPath resolves a sandbox.filesystem path. Unlike Read/Edit rules,
// these use standard conventions: "/" is absolute, "~/" is home, and "./" or no
// prefix is relative to the project root (project settings) or ~/.claude (user
// settings).
func resolveSandboxPath(spec string, scope Scope, ctx ResolveContext) string {
	switch {
	case strings.HasPrefix(spec, "/"):
		return spec
	case strings.HasPrefix(spec, "~/"):
		return joinGlob(ctx.HomeDir, spec[2:])
	default:
		base := ctx.ProjectRoot
		if scope == ScopeUser {
			base = joinGlob(ctx.HomeDir, ".claude")
		}
		return joinGlob(base, strings.TrimPrefix(spec, "./"))
	}
}

// NetworkPolicy is the effective network boundary, merging
// sandbox.network.allowedDomains/deniedDomains with WebFetch allow/deny rules.
type NetworkPolicy struct {
	AllowedDomains []BoundaryEntry
	DeniedDomains  []BoundaryEntry
	// AllowManagedDomainsOnly: non-allowed domains are blocked automatically
	// (no prompt) instead of prompting on first use.
	AllowManagedDomainsOnly bool
}

// ComputeNetworkPolicy folds sandbox.network domains and WebFetch domain rules
// into the effective network policy. When allowManagedDomainsOnly is set, only
// managed-scope allow entries are honored; denied domains always merge.
func ComputeNetworkPolicy(m *MergedSettings) NetworkPolicy {
	np := NetworkPolicy{AllowManagedDomainsOnly: m.AllowManagedDomainsOnly}

	for _, e := range m.NetAllowedDomains {
		np.AllowedDomains = append(np.AllowedDomains, BoundaryEntry{Path: e.Value, Raw: e.Value, Scope: e.Scope, Source: "sandbox.network.allowedDomains"})
	}
	for _, e := range m.NetDeniedDomains {
		np.DeniedDomains = append(np.DeniedDomains, BoundaryEntry{Path: e.Value, Raw: e.Value, Scope: e.Scope, Source: "sandbox.network.deniedDomains"})
	}

	for _, r := range m.Allow {
		if pr, ok := ParseRule(r.Rule); ok && pr.Kind == SpecDomain {
			if m.AllowManagedDomainsOnly && r.Scope != ScopeManaged {
				continue
			}
			np.AllowedDomains = append(np.AllowedDomains, BoundaryEntry{Path: pr.Domain, Raw: r.Rule, Scope: r.Scope, Source: "permissions.allow:WebFetch"})
		}
	}
	for _, r := range m.Deny {
		if pr, ok := ParseRule(r.Rule); ok && pr.Kind == SpecDomain {
			np.DeniedDomains = append(np.DeniedDomains, BoundaryEntry{Path: pr.Domain, Raw: r.Rule, Scope: r.Scope, Source: "permissions.deny:WebFetch"})
		}
	}

	return np
}

// Decide resolves a hostname against the network policy: a denied domain always
// wins; an allowed domain permits; otherwise the call is blocked (when
// allowManagedDomainsOnly) or prompts.
func (np NetworkPolicy) Decide(host string) Decision {
	for _, d := range np.DeniedDomains {
		if matchDomain(d.Path, host) {
			return Decision{Result: ResultDeny, MatchedRule: d.Raw, SourceScope: d.Scope}
		}
	}
	for _, a := range np.AllowedDomains {
		if matchDomain(a.Path, host) {
			return Decision{Result: ResultAllow, MatchedRule: a.Raw, SourceScope: a.Scope}
		}
	}
	if np.AllowManagedDomainsOnly {
		return Decision{Result: ResultDeny}
	}
	return Decision{Result: ResultPrompt}
}
