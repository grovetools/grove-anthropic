package settings

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// buildData assembles a Data snapshot from a fixture tree the same way Load
// does, but with discovery pointed at the fixture rather than the real cwd.
func buildData(t *testing.T, project map[string]string, managed string) *Data {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()

	if managed != "" {
		mp := filepath.Join(root, "managed-settings.json")
		if err := os.WriteFile(mp, []byte(managed), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	claudeDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range project {
		if err := os.WriteFile(filepath.Join(claudeDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	opts := ccsettings.DiscoverOptions{
		ProjectRoot:  root,
		HomeDir:      home,
		ManagedPaths: []string{filepath.Join(root, "managed-settings.json")},
	}
	sources, err := ccsettings.Discover(opts)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	ctx := ccsettings.ResolveContext{CWD: root, ProjectRoot: root, HomeDir: home}
	merged := ccsettings.Merge(sources, ccsettings.MergeOptions{Context: ctx})
	engine := ccsettings.NewEngine(merged, ccsettings.EngineOptions{SandboxAutoAllowBash: merged.SandboxEnabled.Value})
	return &Data{
		Sources: sources,
		Merged:  merged,
		Engine:  engine,
		Ctx:     ctx,
		FS:      ccsettings.ComputeFilesystemBoundary(merged),
		Net:     ccsettings.ComputeNetworkPolicy(merged),
	}
}

const fixtureProject = `{
  "permissions": {
    "allow": ["Bash(git push *)", "Bash(ls *)", "WebFetch(domain:example.com)"],
    "ask": ["Bash(npm *)"],
    "deny": ["Bash(git *)", "Read(.env)"],
    "additionalDirectories": ["/tmp/work"]
  },
  "sandbox": {
    "enabled": true,
    "network": {"deniedDomains": ["evil.test"]}
  },
  "futureKey": {"some": "value"}
}`

func TestConcretize(t *testing.T) {
	cases := map[string]string{
		"git commit *": "git commit x",
		"aws *":        "aws x",
		"*":            "x",
		"**/.env":      "x/.env",
		"plain":        "plain",
	}
	for in, want := range cases {
		if got := concretize(in); got != want {
			t.Errorf("concretize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInferToolCall(t *testing.T) {
	cases := []struct {
		in   string
		tool string
		// one of command/path/url expected non-empty
		field string
	}{
		{"git push origin main", "Bash", "command"},
		{"https://example.com/x", "WebFetch", "url"},
		{"/etc/passwd", "Read", "path"},
		{"./src/main.go", "Read", "path"},
		{"Read: ~/.env", "Read", "path"},
		{"WebFetch: api.example.com", "WebFetch", "url"},
		{"Bash: ls -la", "Bash", "command"},
	}
	for _, c := range cases {
		call := inferToolCall(c.in)
		if call.Tool != c.tool {
			t.Errorf("inferToolCall(%q).Tool = %q, want %q", c.in, call.Tool, c.tool)
		}
		switch c.field {
		case "command":
			if call.Command == "" {
				t.Errorf("inferToolCall(%q) expected a command", c.in)
			}
		case "path":
			if call.Path == "" {
				t.Errorf("inferToolCall(%q) expected a path", c.in)
			}
		case "url":
			if call.URL == "" {
				t.Errorf("inferToolCall(%q) expected a url", c.in)
			}
		}
	}
}

func TestIsShadowed(t *testing.T) {
	d := buildData(t, map[string]string{"settings.json": fixtureProject}, "")

	shadowed, ok := ruleFromMerged(d.Merged.Allow, "Bash(git push *)")
	if !ok {
		t.Fatal("Bash(git push *) allow rule not found")
	}
	if !isShadowed(d.Engine, shadowed) {
		t.Errorf("Bash(git push *) should be shadowed by deny Bash(git *)")
	}

	clean, ok := ruleFromMerged(d.Merged.Allow, "Bash(ls *)")
	if !ok {
		t.Fatal("Bash(ls *) allow rule not found")
	}
	if isShadowed(d.Engine, clean) {
		t.Errorf("Bash(ls *) should not be shadowed")
	}
}

func ruleFromMerged(rules []ccsettings.ProvenancedRule, raw string) (ccsettings.ParsedRule, bool) {
	for _, r := range rules {
		if r.Rule == raw {
			return ccsettings.ParseRule(r.Rule)
		}
	}
	return ccsettings.ParsedRule{}, false
}

func TestPagesRenderWithoutPanic(t *testing.T) {
	d := buildData(t, map[string]string{"settings.json": fixtureProject}, "")
	m := New(d)
	// Drive a window size through the model, then render every tab.
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model := tm.(Model)
	for i := 0; i < len(model.pager.Pages()); i++ {
		model.pager.SetActive(i)
		if got := model.View(); got == "" {
			t.Errorf("page %d rendered empty", i)
		}
	}
}

func TestPrintJSON(t *testing.T) {
	d := buildData(t, map[string]string{"settings.json": fixtureProject}, "")
	var buf bytes.Buffer
	if err := PrintJSON(d, &buf); err != nil {
		t.Fatalf("PrintJSON: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, key := range []string{"context", "sources", "permissions", "sandbox"} {
		if _, ok := out[key]; !ok {
			t.Errorf("JSON output missing %q", key)
		}
	}
	// Provenance must reach individual rules.
	if !strings.Contains(buf.String(), "\"Project\"") {
		t.Errorf("expected Project provenance in JSON output")
	}
}

func TestManagedReadOnlyAndDrift(t *testing.T) {
	managed := `{"permissions":{"deny":["Bash"],"allowManagedPermissionRulesOnly":true}}`
	d := buildData(t, map[string]string{"settings.json": fixtureProject}, managed)

	if !d.Merged.AllowManagedPermissionRulesOnly {
		t.Errorf("expected managed lockdown to be detected")
	}
	// With allowManagedPermissionRulesOnly, only managed rules survive the merge.
	for _, r := range d.Merged.Deny {
		if r.Scope != ccsettings.ScopeManaged {
			t.Errorf("non-managed rule %q survived managed lockdown", r.Rule)
		}
	}

	// The effective page should report removed-from-context tools and drift.
	ep := newEffectivePage(d)
	ep.SetSize(100, 30)
	view := ep.View()
	if !strings.Contains(view, "removed from context") {
		t.Errorf("effective view missing removed-from-context section")
	}
}
