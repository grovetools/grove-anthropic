package ccsettings

import "testing"

func hasEntry(entries []BoundaryEntry, source, raw string) bool {
	for _, e := range entries {
		if e.Source == source && e.Raw == raw {
			return true
		}
	}
	return false
}

func TestComputeFilesystemBoundary(t *testing.T) {
	m := mergedFromJSON(t, map[Scope]string{
		ScopeProject: `{
			"permissions":{
				"allow":["Edit(/build/**)"],
				"deny":["Read(~/.ssh/**)","Edit(/protected/**)"],
				"additionalDirectories":["/work/extra"]
			},
			"sandbox":{"filesystem":{"allowWrite":["~/.kube","/tmp/build"],"denyRead":["~/secrets"]}}
		}`,
	})
	fb := ComputeFilesystemBoundary(m)

	if !hasEntry(fb.AllowWrite, "sandbox.filesystem.allowWrite", "~/.kube") {
		t.Errorf("missing sandbox allowWrite ~/.kube: %+v", fb.AllowWrite)
	}
	if !hasEntry(fb.AllowWrite, "permissions.allow:Edit", "Edit(/build/**)") {
		t.Errorf("Edit allow should grant write: %+v", fb.AllowWrite)
	}
	if !hasEntry(fb.AllowWrite, "additionalDirectories", "/work/extra") {
		t.Errorf("additionalDirectories should be writable: %+v", fb.AllowWrite)
	}
	if !hasEntry(fb.AllowRead, "additionalDirectories", "/work/extra") {
		t.Errorf("additionalDirectories should be readable: %+v", fb.AllowRead)
	}
	if !hasEntry(fb.DenyRead, "permissions.deny:Read", "Read(~/.ssh/**)") {
		t.Errorf("Read deny should block read: %+v", fb.DenyRead)
	}
	if !hasEntry(fb.DenyWrite, "permissions.deny:Edit", "Edit(/protected/**)") {
		t.Errorf("Edit deny should block write: %+v", fb.DenyWrite)
	}
	if !hasEntry(fb.DenyRead, "sandbox.filesystem.denyRead", "~/secrets") {
		t.Errorf("sandbox denyRead should be present: %+v", fb.DenyRead)
	}
	// The working directory is an implicit writable+readable default.
	if !hasEntry(fb.AllowWrite, "default:workingDirectory", ".") {
		t.Errorf("working directory should be a default writable entry: %+v", fb.AllowWrite)
	}
}

func TestComputeNetworkPolicyAndDecide(t *testing.T) {
	m := mergedFromJSON(t, map[Scope]string{
		ScopeProject: `{
			"permissions":{"allow":["WebFetch(domain:*.example.com)"],"deny":["WebFetch(domain:bad.example.com)"]},
			"sandbox":{"network":{"allowedDomains":["registry.npmjs.org"],"deniedDomains":["tracker.test"]}}
		}`,
	})
	np := ComputeNetworkPolicy(m)

	if !hasEntry(np.AllowedDomains, "sandbox.network.allowedDomains", "registry.npmjs.org") {
		t.Errorf("missing sandbox allowedDomains: %+v", np.AllowedDomains)
	}
	if !hasEntry(np.AllowedDomains, "permissions.allow:WebFetch", "WebFetch(domain:*.example.com)") {
		t.Errorf("WebFetch allow should fold into allowedDomains: %+v", np.AllowedDomains)
	}

	if d := np.Decide("api.example.com"); d.Result != ResultAllow {
		t.Errorf("api.example.com should be allowed, got %+v", d)
	}
	if d := np.Decide("bad.example.com"); d.Result != ResultDeny {
		t.Errorf("bad.example.com should be denied, got %+v", d)
	}
	if d := np.Decide("unknown.test"); d.Result != ResultPrompt {
		t.Errorf("unknown domain should prompt (no managed lockdown), got %+v", d)
	}
}

func TestComputeNetworkPolicyManagedDomainsOnly(t *testing.T) {
	m := mergedFromJSON(t, map[Scope]string{
		ScopeManaged: `{"sandbox":{"network":{"allowManagedDomainsOnly":true,"allowedDomains":["corp.example.com"]}}}`,
		ScopeUser:    `{"permissions":{"allow":["WebFetch(domain:widen.example.com)"]}}`,
	})
	np := ComputeNetworkPolicy(m)

	// The user's WebFetch allow is dropped under managed-domains-only.
	if hasEntry(np.AllowedDomains, "permissions.allow:WebFetch", "WebFetch(domain:widen.example.com)") {
		t.Errorf("user WebFetch allow should be dropped under managed lockdown: %+v", np.AllowedDomains)
	}
	// Non-allowed domains are blocked without prompting.
	if d := np.Decide("widen.example.com"); d.Result != ResultDeny {
		t.Errorf("non-allowed domain should be blocked under managed lockdown, got %+v", d)
	}
	if d := np.Decide("corp.example.com"); d.Result != ResultAllow {
		t.Errorf("managed-allowed domain should be allowed, got %+v", d)
	}
}
