package models

import "testing"

// LatestAliasForFamily must track Models() order (newest-first per family) and
// skip legacy entries, so callers naming a family get the current member rather
// than a pinned version.
func TestLatestAliasForFamily(t *testing.T) {
	cases := []struct {
		family string
		want   string
		wantOK bool
	}{
		{"opus", "claude-opus-5", true},
		{"OPUS", "claude-opus-5", true}, // case-insensitive
		{" sonnet ", "claude-sonnet-5", true},
		{"fable", "claude-fable-5", true},
		{"haiku", "claude-haiku-4-5", true},
		{"gpt", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := LatestAliasForFamily(tc.family)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("LatestAliasForFamily(%q) = (%q, %v), want (%q, %v)",
				tc.family, got, ok, tc.want, tc.wantOK)
		}
	}
}

// Every alias the family lookup can return must be a real registry alias —
// guards against a family whose newest entry has an empty Alias.
func TestLatestAliasForFamilyReturnsKnownAlias(t *testing.T) {
	aliases := Aliases()
	for _, family := range []string{"opus", "sonnet", "haiku", "fable"} {
		alias, ok := LatestAliasForFamily(family)
		if !ok {
			t.Errorf("family %q has no current model", family)
			continue
		}
		if _, known := aliases[alias]; !known {
			t.Errorf("LatestAliasForFamily(%q) = %q, not a registry alias", family, alias)
		}
	}
}
