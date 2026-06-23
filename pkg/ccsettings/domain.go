package ccsettings

import (
	"net/url"
	"strings"
)

// hostFromURL extracts the lowercased hostname from a requested URL. A bare
// host (no scheme) is accepted as-is.
func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return strings.ToLower(u.Hostname())
	}
	// No scheme: treat the whole string as a host (strip any path).
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		raw = raw[:i]
	}
	return strings.ToLower(raw)
}

// matchDomain reports whether a WebFetch domain pattern matches a hostname.
//
//   - "*" matches every domain.
//   - "*.example.com" matches any subdomain at any depth (api.example.com,
//     a.b.example.com) but NOT example.com itself.
//   - In any other position the wildcard matches only the text between two
//     dots, so "example.*" matches "example.org" but not "example.evil.com".
//
// Matching is case-insensitive and a trailing "." is stripped from both sides.
func matchDomain(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if pattern == "" || host == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if rest, ok := strings.CutPrefix(pattern, "*."); ok {
		// Subdomain match at any depth, excluding the apex itself.
		return strings.HasSuffix(host, "."+rest)
	}

	// Label-by-label match; a "*" within a label matches only within that one
	// label and never crosses a dot, so label counts must be equal.
	pLabels := strings.Split(pattern, ".")
	hLabels := strings.Split(host, ".")
	if len(pLabels) != len(hLabels) {
		return false
	}
	for i := range pLabels {
		if !matchLabel(pLabels[i], hLabels[i]) {
			return false
		}
	}
	return true
}

// matchLabel matches a single DNS label where "*" stands for any run of
// characters that are not a dot.
func matchLabel(pat, label string) bool {
	if !strings.Contains(pat, "*") {
		return pat == label
	}
	parts := strings.Split(pat, "*")
	// Must start with parts[0] and end with the last part, consuming the
	// middle greedily without crossing into another label (no dots possible
	// here since the label was split on ".").
	if !strings.HasPrefix(label, parts[0]) {
		return false
	}
	label = label[len(parts[0]):]
	last := parts[len(parts)-1]
	if !strings.HasSuffix(label, last) {
		return false
	}
	label = label[:len(label)-len(last)]
	for _, mid := range parts[1 : len(parts)-1] {
		idx := strings.Index(label, mid)
		if idx < 0 {
			return false
		}
		label = label[idx+len(mid):]
	}
	return true
}
