package ccsettings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedSettingsPaths(t *testing.T) {
	tests := []struct {
		goos string
		want string
	}{
		{"darwin", "/Library/Application Support/ClaudeCode/managed-settings.json"},
		{"linux", "/etc/claude-code/managed-settings.json"},
		{"windows", `C:\Program Files\ClaudeCode\managed-settings.json`},
	}
	for _, tt := range tests {
		got := managedSettingsPaths(tt.goos)
		if len(got) == 0 || got[0] != tt.want {
			t.Errorf("managedSettingsPaths(%q) = %v, want first %q", tt.goos, got, tt.want)
		}
	}
}

func TestDiscoverAndMergeFixtureTree(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	managed := filepath.Join(root, "managed", "managed-settings.json")

	writeFixture(t, filepath.Join(home, ".claude", "settings.json"),
		`{"permissions":{"allow":["Bash(ls *)"],"defaultMode":"default"}}`)
	writeFixture(t, filepath.Join(project, ".claude", "settings.json"),
		`{"permissions":{"deny":["Bash(rm *)"],"defaultMode":"plan"}}`)
	writeFixture(t, filepath.Join(project, ".claude", "settings.local.json"),
		`{"permissions":{"allow":["Bash(git *)"]}}`)
	writeFixture(t, managed,
		`{"permissions":{"deny":["Bash(curl *)"]}}`)

	sources, err := Discover(DiscoverOptions{
		ProjectRoot:  project,
		HomeDir:      home,
		ManagedPaths: []string{managed},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	got := map[Scope]bool{}
	for _, sf := range sources {
		if sf.Exists {
			got[sf.Scope] = true
		}
		if sf.ParseError != nil {
			t.Errorf("%s parse error: %v", sf.Scope.Label(), sf.ParseError)
		}
	}
	for _, want := range []Scope{ScopeManaged, ScopeUser, ScopeProject, ScopeLocal} {
		if !got[want] {
			t.Errorf("scope %s not discovered as existing", want.Label())
		}
	}

	m := Merge(sources, MergeOptions{Context: ResolveContext{CWD: project, ProjectRoot: project, HomeDir: home}})
	// Allow rules merge additively from Local and User.
	if len(m.Allow) != 2 {
		t.Errorf("Allow = %+v, want 2 (local + user)", m.Allow)
	}
	// Deny merges from Managed and Project.
	if len(m.Deny) != 2 {
		t.Errorf("Deny = %+v, want 2 (managed + project)", m.Deny)
	}
	// defaultMode: Project (plan) outranks User (default); no Local/Managed value.
	if m.DefaultMode.Value != "plan" || m.DefaultMode.Scope != ScopeProject {
		t.Errorf("DefaultMode = %+v, want plan from Project", m.DefaultMode)
	}
}

func TestDiscoverToleratesMissingAndMalformed(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")

	// Malformed project file; everything else absent.
	writeFixture(t, filepath.Join(project, ".claude", "settings.json"), `{not json`)

	sources, err := Discover(DiscoverOptions{
		ProjectRoot:  project,
		HomeDir:      home,
		ManagedPaths: []string{filepath.Join(root, "none.json")},
	})
	if err != nil {
		t.Fatalf("Discover should not fail on malformed/missing files: %v", err)
	}
	var sawParseErr bool
	for _, sf := range sources {
		if sf.Scope == ScopeProject {
			if sf.ParseError == nil {
				t.Errorf("expected parse error for malformed project file")
			}
			sawParseErr = true
		}
	}
	if !sawParseErr {
		t.Errorf("project source not present in discovery output")
	}
	// A merge over a malformed source simply omits it without panicking.
	_ = Merge(sources, MergeOptions{})
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
