package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadFromAnchorsOnGivenDir verifies LoadFrom resolves the Project scope at
// the passed directory's project root, not the process cwd.
func TestLoadFromAnchorsOnGivenDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "myproj")
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".claude", "settings.json"), []byte(`{"permissions":{"allow":["Bash(ls *)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := LoadFrom(proj)
	if err != nil {
		t.Fatal(err)
	}
	if data.Ctx.ProjectRoot != proj {
		t.Errorf("ProjectRoot = %q, want %q", data.Ctx.ProjectRoot, proj)
	}
	if data.Ctx.CWD != proj {
		t.Errorf("CWD = %q, want %q", data.Ctx.CWD, proj)
	}
}
