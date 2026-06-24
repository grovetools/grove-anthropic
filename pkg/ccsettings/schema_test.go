package ccsettings

import (
	"encoding/json"
	"testing"
)

// TestSchemaLoads asserts the vendored schema embeds and parses into a non-empty
// index with the expected shape (a representative key with type + description).
func TestSchemaLoads(t *testing.T) {
	idx := SchemaIndex()
	if len(idx) == 0 {
		t.Fatal("schema index is empty; vendored schema failed to embed or parse")
	}

	// effortLevel is a stable string-enum key in the schema.
	el, ok := idx["effortLevel"]
	if !ok {
		t.Fatal("schema index missing effortLevel")
	}
	if el.Type != "string" {
		t.Errorf("effortLevel type = %q, want string", el.Type)
	}
	if el.Description == "" {
		t.Error("effortLevel description is empty")
	}
	if len(el.Enum) == 0 {
		t.Error("effortLevel enum is empty; expected low/medium/high/...")
	}
}

// TestSchemaDrift is the forcing function: every top-level property in the
// vendored schema must be either TYPED by our Go model or listed in the
// intentionalPassthrough allowlist. A schema key that is neither fails this
// test, forcing a conscious classification whenever the schema is refreshed or
// Anthropic adds a key.
func TestSchemaDrift(t *testing.T) {
	typed := typedTopLevelKeys()
	allow := intentionalPassthroughSet()

	var unclassified []string
	for key := range SchemaIndex() {
		// $schema is the JSON Schema reference key; our model types it.
		if _, ok := typed[key]; ok {
			continue
		}
		if _, ok := allow[key]; ok {
			continue
		}
		unclassified = append(unclassified, key)
	}

	if len(unclassified) > 0 {
		t.Fatalf("schema keys neither typed nor allowlisted (classify each — type it in types.go "+
			"or add to intentionalPassthrough in schema.go): %v", unclassified)
	}
}

// TestIntentionalPassthroughIsClean guards the allowlist against two kinds of
// rot: an entry that no longer exists in the schema (dead entry) and an entry
// that is now typed by the model (should be removed from the allowlist).
func TestIntentionalPassthroughIsClean(t *testing.T) {
	idx := SchemaIndex()
	typed := typedTopLevelKeys()
	for _, key := range intentionalPassthrough {
		if _, ok := idx[key]; !ok {
			t.Errorf("intentionalPassthrough key %q is not in the vendored schema (dead entry)", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("intentionalPassthrough key %q is now typed by the model; remove it from the allowlist", key)
		}
	}
}

// TestSkipDangerousModePromoted verifies the permission-adjacent key was
// promoted into the typed model (not left in passthrough) and classifies as
// typed.
func TestSkipDangerousModePromoted(t *testing.T) {
	if _, ok := typedTopLevelKeys()["skipDangerousModePermissionPrompt"]; !ok {
		t.Fatal("skipDangerousModePermissionPrompt must be typed in the model, not passthrough")
	}
	ck := ClassifyKey("skipDangerousModePermissionPrompt")
	if ck.Class != ClassTyped {
		t.Errorf("skipDangerousModePermissionPrompt class = %v, want ClassTyped", ck.Class)
	}
}

// TestClassifyBuckets exercises the three-bucket classifier with a hand-picked
// key from each bucket.
func TestClassifyBuckets(t *testing.T) {
	cases := []struct {
		key  string
		want KeyClass
	}{
		{"permissions", ClassTyped},                 // typed by the model
		{"tui", ClassPassthroughKnown},              // in schema, intentionally not typed
		{"definitelyNotARealKey_xyz", ClassUnknown}, // in neither
	}
	for _, c := range cases {
		if got := ClassifyKey(c.key).Class; got != c.want {
			t.Errorf("ClassifyKey(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

// TestClassifySourcesCounts builds a settings file with one key from each bucket
// and asserts the coverage counts and per-key schema enrichment.
func TestClassifySourcesCounts(t *testing.T) {
	raw := []byte(`{
		"permissions": {"allow": ["Bash"]},
		"tui": "fullscreen",
		"someBrandNewKey": true
	}`)
	s, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sources := []SourceFile{{Scope: ScopeProject, Exists: true, Settings: s, Raw: raw}}

	classified, cov := ClassifySources(sources)
	if cov.Typed != 1 || cov.PassthroughKnown != 1 || cov.Unknown != 1 || cov.Total != 3 {
		t.Fatalf("coverage = %+v, want typed=1 passthroughKnown=1 unknown=1 total=3", cov)
	}

	byKey := map[string]ClassifiedKey{}
	for _, ck := range classified {
		byKey[ck.Key] = ck
	}
	if got := byKey["tui"]; got.Class != ClassPassthroughKnown || got.Schema == nil {
		t.Errorf("tui = %+v, want passthrough_known with schema detail", got)
	} else if len(got.Schema.Enum) == 0 {
		t.Errorf("tui schema enum is empty; expected fullscreen/default")
	}
	if got := byKey["someBrandNewKey"]; got.Class != ClassUnknown || got.Schema != nil {
		t.Errorf("someBrandNewKey = %+v, want unknown with no schema", got)
	}
}

// TestSkipDangerousModeRoundTrips verifies the promoted key still round-trips
// through marshal/unmarshal (it must not be lost on write).
func TestSkipDangerousModeRoundTrips(t *testing.T) {
	raw := []byte(`{"skipDangerousModePermissionPrompt": true}`)
	s, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.SkipDangerousModePermissionPrompt == nil || !*s.SkipDangerousModePermissionPrompt {
		t.Fatalf("skipDangerousModePermissionPrompt not decoded as true: %+v", s.SkipDangerousModePermissionPrompt)
	}
	out, err := s.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal back: %v", err)
	}
	if v, ok := back["skipDangerousModePermissionPrompt"].(bool); !ok || !v {
		t.Errorf("round-trip lost skipDangerousModePermissionPrompt: %v", back["skipDangerousModePermissionPrompt"])
	}
}
