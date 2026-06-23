package ccsettings

import "testing"

func TestMatchDomain(t *testing.T) {
	tests := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "EXAMPLE.COM", true},
		{"example.com", "example.com.", true}, // trailing dot stripped
		{"example.com", "api.example.com", false},
		// Subdomain wildcard: any depth, but not the apex.
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},
		// Wildcard between dots matches only one label.
		{"example.*", "example.org", true},
		{"example.*", "example.evil.com", false},
		// Match-all.
		{"*", "anything.test", true},
	}
	for _, tt := range tests {
		if got := matchDomain(tt.pattern, tt.host); got != tt.want {
			t.Errorf("matchDomain(%q, %q) = %v, want %v", tt.pattern, tt.host, got, tt.want)
		}
	}
}

func TestHostFromURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://API.Example.com/path", "api.example.com"},
		{"http://example.com", "example.com"},
		{"example.com/foo", "example.com"},
		{"example.com", "example.com"},
	}
	for _, tt := range tests {
		if got := hostFromURL(tt.in); got != tt.want {
			t.Errorf("hostFromURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
