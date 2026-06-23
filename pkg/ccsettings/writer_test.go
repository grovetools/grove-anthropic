package ccsettings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The crux requirement: editing one deep value must leave every other key,
// the key order, and hand-authored comments byte-for-byte intact.
func TestApplyEditsPreservesCommentsOrderUnknownKeys(t *testing.T) {
	src := `{
  // top-of-file comment
  "futureKey": "keep-me", // trailing comment on unknown key
  "permissions": {
    "defaultMode": "acceptEdits",
    "allow": [
      "Read", // inline comment in array
      "Bash(ls:*)"
    ],
    "ask": ["WebFetch"],
    "additionalDirectories": ["/tmp"]
  },
  "sandbox": {
    "enabled": false,
    "network": {
      "allowedDomains": ["example.com"]
    }
  },
}`

	out, err := ApplyEdits([]byte(src), []Edit{
		{Op: OpSet, Path: []string{"sandbox", "enabled"}, ValueKind: KindBool, BoolVal: true},
	})
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	got := string(out)

	// The edited value changed.
	if !strings.Contains(got, `"enabled": true`) {
		t.Errorf("expected sandbox.enabled to become true, got:\n%s", got)
	}
	if strings.Contains(got, `"enabled": false`) {
		t.Errorf("old value should be gone, got:\n%s", got)
	}

	// Comments survive.
	for _, want := range []string{
		"// top-of-file comment",
		"// trailing comment on unknown key",
		"// inline comment in array",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("comment %q was stripped, got:\n%s", want, got)
		}
	}

	// Unknown key survives verbatim.
	if !strings.Contains(got, `"futureKey": "keep-me"`) {
		t.Errorf("unknown key futureKey was dropped, got:\n%s", got)
	}

	// Key order is preserved: futureKey before permissions before sandbox;
	// within permissions, defaultMode before allow before ask before
	// additionalDirectories.
	assertOrder(t, got,
		"futureKey", "permissions", "defaultMode", "allow", "ask",
		"additionalDirectories", "sandbox", "enabled", "network", "allowedDomains",
	)

	// Untouched array contents preserved.
	for _, want := range []string{`"Read"`, `"Bash(ls:*)"`, `"WebFetch"`, `"/tmp"`, `"example.com"`} {
		if !strings.Contains(got, want) {
			t.Errorf("untouched value %q lost, got:\n%s", want, got)
		}
	}
}

func TestApplyEditsArrayAppendRemove(t *testing.T) {
	src := `{"permissions":{"allow":["Read"]}}`

	// Append a new element.
	out, err := ApplyEdits([]byte(src), []Edit{
		{Op: OpArrayAppend, Path: []string{"permissions", "allow"}, StringVal: "Bash(ls:*)"},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !strings.Contains(string(out), "Bash(ls:*)") {
		t.Errorf("append did not add element: %s", out)
	}

	// Appending a duplicate is a no-op.
	out2, err := ApplyEdits(out, []Edit{
		{Op: OpArrayAppend, Path: []string{"permissions", "allow"}, StringVal: "Bash(ls:*)"},
	})
	if err != nil {
		t.Fatalf("append dup: %v", err)
	}
	if strings.Count(string(out2), "Bash(ls:*)") != 1 {
		t.Errorf("duplicate append should be no-op: %s", out2)
	}

	// Remove an element.
	out3, err := ApplyEdits(out2, []Edit{
		{Op: OpArrayRemove, Path: []string{"permissions", "allow"}, StringVal: "Read"},
	})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if strings.Contains(string(out3), `"Read"`) {
		t.Errorf("remove did not delete element: %s", out3)
	}
}

func TestApplyEditsCreatesNestedPath(t *testing.T) {
	// Edits to a non-existent file / empty object create intermediate objects.
	out, err := ApplyEdits(nil, []Edit{
		{Op: OpArrayAppend, Path: []string{"permissions", "additionalDirectories"}, StringVal: "/srv"},
		{Op: OpSet, Path: []string{"sandbox", "enabled"}, ValueKind: KindBool, BoolVal: true},
	})
	if err != nil {
		t.Fatalf("create nested: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "/srv") || !strings.Contains(got, `"enabled": true`) {
		t.Errorf("nested creation failed: %s", got)
	}
}

func TestApplyEditsUnset(t *testing.T) {
	src := `{"permissions":{"defaultMode":"acceptEdits","allow":["Read"]}}`
	out, err := ApplyEdits([]byte(src), []Edit{
		{Op: OpUnset, Path: []string{"permissions", "defaultMode"}},
	})
	if err != nil {
		t.Fatalf("unset: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "defaultMode") {
		t.Errorf("unset did not remove key: %s", got)
	}
	if !strings.Contains(got, `"Read"`) {
		t.Errorf("unset removed sibling: %s", got)
	}
}

func TestWriteEditsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "settings.json")

	if err := WriteEdits(path, []Edit{
		{Op: OpSet, Path: []string{"sandbox", "enabled"}, ValueKind: KindBool, BoolVal: true},
	}); err != nil {
		t.Fatalf("WriteEdits: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), `"enabled": true`) {
		t.Errorf("written file missing edit: %s", data)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".settings-") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

// assertOrder checks that the given substrings appear in the given order.
func assertOrder(t *testing.T, s string, keys ...string) {
	t.Helper()
	last := 0
	for _, k := range keys {
		idx := strings.Index(s[last:], `"`+k+`"`)
		if idx < 0 {
			t.Errorf("key %q not found after offset %d in:\n%s", k, last, s)
			return
		}
		last += idx + len(k)
	}
}
