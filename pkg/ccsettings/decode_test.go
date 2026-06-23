package ccsettings

import (
	"encoding/json"
	"testing"
)

func TestParsePreservesUnknownKeys(t *testing.T) {
	in := []byte(`{
		"model": "claude-sonnet-4-6",
		"permissions": {"allow": ["Bash(ls *)"], "futureScopedKey": {"a": 1}},
		"someBrandNewKey": {"nested": [1, 2, 3]},
		"anotherNewToggle": true
	}`)

	s, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want claude-sonnet-4-6", s.Model)
	}
	if _, ok := s.Unknown["someBrandNewKey"]; !ok {
		t.Errorf("someBrandNewKey not preserved in Unknown: %v", s.Unknown)
	}
	if _, ok := s.Unknown["anotherNewToggle"]; !ok {
		t.Errorf("anotherNewToggle not preserved in Unknown")
	}
	if s.Permissions == nil || len(s.Permissions.Unknown) == 0 {
		t.Fatalf("nested unknown key not preserved: %+v", s.Permissions)
	}
	if _, ok := s.Permissions.Unknown["futureScopedKey"]; !ok {
		t.Errorf("nested futureScopedKey not preserved: %v", s.Permissions.Unknown)
	}
}

func TestRoundTripPreservesUnknownKeys(t *testing.T) {
	in := []byte(`{"model":"m","someBrandNewKey":{"nested":[1,2,3]},"permissions":{"deny":["Read(.env)"],"futureKey":42}}`)
	s, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if _, ok := got["someBrandNewKey"]; !ok {
		t.Errorf("someBrandNewKey lost on round-trip: %s", out)
	}
	// Confirm the unknown key value survived intact.
	var perms map[string]json.RawMessage
	if err := json.Unmarshal(got["permissions"], &perms); err != nil {
		t.Fatalf("permissions re-parse: %v", err)
	}
	if string(perms["futureKey"]) != "42" {
		t.Errorf("nested futureKey = %s, want 42", perms["futureKey"])
	}
}

func TestTolerantDecodeTypeMismatchPreserved(t *testing.T) {
	// A future Claude changes cleanupPeriodDays from a number to an object;
	// the value must still be preserved (not dropped) and flagged.
	in := []byte(`{"model":"m","cleanupPeriodDays":{"unexpected":"shape"}}`)
	s, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse should tolerate type mismatch, got: %v", err)
	}
	if s.Model != "m" {
		t.Errorf("Model = %q, want m (other keys must still decode)", s.Model)
	}
	if _, ok := s.Unknown["cleanupPeriodDays"]; !ok {
		t.Errorf("mismatched cleanupPeriodDays should be preserved in Unknown")
	}
	if len(s.DecodeWarnings) == 0 {
		t.Errorf("expected a decode warning for the type mismatch")
	}
}

func TestParseEmpty(t *testing.T) {
	for _, in := range []string{"", "  \n ", "{}"} {
		s, err := Parse([]byte(in))
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
		}
		if s == nil {
			t.Errorf("Parse(%q): nil settings", in)
		}
	}
}
