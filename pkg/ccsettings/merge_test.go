package ccsettings

import "testing"

// mergedFromJSON builds a MergedSettings from per-scope JSON documents, using a
// fixed resolve context. CLI is supplied via the ScopeCLI key.
func mergedFromJSON(t *testing.T, docs map[Scope]string) *MergedSettings {
	t.Helper()
	var sources []SourceFile
	var cli *Settings
	for scope, doc := range docs {
		s, err := Parse([]byte(doc))
		if err != nil {
			t.Fatalf("parse %s: %v", scope.Label(), err)
		}
		if scope == ScopeCLI {
			cli = s
			continue
		}
		sources = append(sources, SourceFile{Scope: scope, Path: scope.Label(), Exists: true, Settings: s})
	}
	return Merge(sources, MergeOptions{CLI: cli, Context: ctxFixture()})
}

func TestMergeScalarPrecedence(t *testing.T) {
	m := mergedFromJSON(t, map[Scope]string{
		ScopeUser:    `{"permissions":{"defaultMode":"default"}}`,
		ScopeProject: `{"permissions":{"defaultMode":"plan"}}`,
		ScopeLocal:   `{"permissions":{"defaultMode":"acceptEdits"}}`,
	})
	// Local outranks Project and User.
	if m.DefaultMode.Value != "acceptEdits" {
		t.Errorf("DefaultMode = %q, want acceptEdits", m.DefaultMode.Value)
	}
	if m.DefaultMode.Scope != ScopeLocal {
		t.Errorf("DefaultMode scope = %v, want Local", m.DefaultMode.Scope)
	}
}

func TestMergeManagedBeatsCLI(t *testing.T) {
	m := mergedFromJSON(t, map[Scope]string{
		ScopeCLI:     `{"permissions":{"defaultMode":"bypassPermissions"}}`,
		ScopeManaged: `{"permissions":{"defaultMode":"default"}}`,
	})
	if m.DefaultMode.Value != "default" || m.DefaultMode.Scope != ScopeManaged {
		t.Errorf("DefaultMode = %+v, want default from Managed", m.DefaultMode)
	}
}

func TestMergeArraysAdditiveWithProvenance(t *testing.T) {
	m := mergedFromJSON(t, map[Scope]string{
		ScopeUser:    `{"permissions":{"deny":["Read(.env)"]}}`,
		ScopeProject: `{"permissions":{"deny":["Bash(rm *)"]}}`,
	})
	if len(m.Deny) != 2 {
		t.Fatalf("Deny has %d entries, want 2: %+v", len(m.Deny), m.Deny)
	}
	// Project (higher precedence) is listed first.
	if m.Deny[0].Rule != "Bash(rm *)" || m.Deny[0].Scope != ScopeProject {
		t.Errorf("Deny[0] = %+v, want Bash(rm *) from Project", m.Deny[0])
	}
	if m.Deny[1].Rule != "Read(.env)" || m.Deny[1].Scope != ScopeUser {
		t.Errorf("Deny[1] = %+v, want Read(.env) from User", m.Deny[1])
	}
}

func TestMergeManagedPermissionRulesOnly(t *testing.T) {
	m := mergedFromJSON(t, map[Scope]string{
		ScopeManaged: `{"permissions":{"allowManagedPermissionRulesOnly":true,"allow":["Bash(ls *)"]}}`,
		ScopeUser:    `{"permissions":{"allow":["Bash(rm *)"],"deny":["Read(.env)"]}}`,
	})
	if !m.AllowManagedPermissionRulesOnly {
		t.Fatalf("AllowManagedPermissionRulesOnly should be true")
	}
	if len(m.Allow) != 1 || m.Allow[0].Scope != ScopeManaged {
		t.Errorf("Allow = %+v, want only the managed rule", m.Allow)
	}
	if len(m.Deny) != 0 {
		t.Errorf("Deny = %+v, want empty (non-managed rules subtracted)", m.Deny)
	}
}

func TestMergeSandboxArraysAndLockdowns(t *testing.T) {
	m := mergedFromJSON(t, map[Scope]string{
		ScopeManaged: `{"sandbox":{"filesystem":{"allowManagedReadPathsOnly":true,"allowRead":["/managed/ok"]},"network":{"allowManagedDomainsOnly":true,"allowedDomains":["managed.example.com"]}}}`,
		ScopeUser:    `{"sandbox":{"filesystem":{"allowRead":["~/widen"],"denyRead":["~/.ssh"]},"network":{"allowedDomains":["user.example.com"],"deniedDomains":["evil.example.com"]}}}`,
	})
	if !m.AllowManagedReadPathsOnly || !m.AllowManagedDomainsOnly {
		t.Fatalf("managed lockdown flags not resolved: %+v", m)
	}
	// allowRead locked to managed; denyRead still merges.
	if len(m.FSAllowRead) != 1 || m.FSAllowRead[0].Scope != ScopeManaged {
		t.Errorf("FSAllowRead = %+v, want only managed", m.FSAllowRead)
	}
	if len(m.FSDenyRead) != 1 || m.FSDenyRead[0].Scope != ScopeUser {
		t.Errorf("FSDenyRead = %+v, want the user denyRead", m.FSDenyRead)
	}
	// allowedDomains locked to managed; deniedDomains still merges.
	if len(m.NetAllowedDomains) != 1 || m.NetAllowedDomains[0].Scope != ScopeManaged {
		t.Errorf("NetAllowedDomains = %+v, want only managed", m.NetAllowedDomains)
	}
	if len(m.NetDeniedDomains) != 1 {
		t.Errorf("NetDeniedDomains = %+v, want the user deny", m.NetDeniedDomains)
	}
}
