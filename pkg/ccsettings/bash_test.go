package ccsettings

import (
	"reflect"
	"testing"
)

func TestMatchBashPattern(t *testing.T) {
	tests := []struct {
		pattern string
		cmd     string
		want    bool
	}{
		// Word boundary: " *" requires a space or end-of-string.
		{"ls *", "ls -la", true},
		{"ls *", "lsof", false},
		{"ls *", "ls", true},
		// No space: matches without a word boundary.
		{"ls*", "lsof", true},
		{"ls*", "ls -la", true},
		// ":*" suffix is equivalent to " *".
		{"ls:*", "ls -la", true},
		{"ls:*", "lsof", false},
		// Exact match.
		{"npm run build", "npm run build", true},
		{"npm run build", "npm run build --watch", false},
		// Wildcards at various positions.
		{"npm run *", "npm run test", true},
		{"npm *", "npm install", true},
		{"* install", "npm install", true},
		{"git * main", "git checkout main", true},
		{"git * main", "git push origin main", true},
		{"git * main", "git checkout dev", false},
		{"* --version", "node --version", true},
		// The headline shadowing example.
		{"aws *", "aws s3 ls", true},
		{"aws *", "aws", true},
		{"aws *", "awsx", false},
	}
	for _, tt := range tests {
		if got := matchBashPattern(tt.pattern, tt.cmd); got != tt.want {
			t.Errorf("matchBashPattern(%q, %q) = %v, want %v", tt.pattern, tt.cmd, got, tt.want)
		}
	}
}

func TestSplitCompound(t *testing.T) {
	tests := []struct {
		cmd  string
		want []string
	}{
		{"git status && npm test", []string{"git status", "npm test"}},
		{"a || b ; c | d", []string{"a", "b", "c", "d"}},
		{"a |& b & c", []string{"a", "b", "c"}},
		{"a\nb", []string{"a", "b"}},
		{"single", []string{"single"}},
	}
	for _, tt := range tests {
		if got := SplitCompound(tt.cmd); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SplitCompound(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

func TestStripWrappers(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"timeout 30 npm test", "npm test"},
		{"timeout -s SIGKILL 30 npm test", "npm test"},
		{"nohup npm start", "npm start"},
		{"nice -n 10 make", "make"},
		{"time go build", "go build"},
		{"xargs grep pattern", "grep pattern"},
		// xargs WITH flags is not stripped.
		{"xargs -n1 grep pattern", "xargs -n1 grep pattern"},
		// Not a wrapper.
		{"npm test", "npm test"},
		// Nested wrappers.
		{"timeout 5 nohup npm test", "npm test"},
	}
	for _, tt := range tests {
		if got := StripWrappers(tt.cmd); got != tt.want {
			t.Errorf("StripWrappers(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestSplitCompoundKeepsRedirections(t *testing.T) {
	// 2>&1 must not be split on its inner & into a phantom `1` subcommand.
	got := SplitCompound("git status 2>&1 | head -30")
	want := []string{"git status 2>&1", "head -30"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitCompound = %q, want %q", got, want)
	}
	// A real background & still splits.
	if got := SplitCompound("sleep 1 & echo hi"); !reflect.DeepEqual(got, []string{"sleep 1", "echo hi"}) {
		t.Errorf("background split = %q", got)
	}
}

func TestStripRedirections(t *testing.T) {
	cases := map[string]string{
		"git status 2>&1":       "git status",
		"cd /x 2>/dev/null":     "cd /x",
		"cmd 2>/dev/null > out": "cmd",
		"echo hi":               "echo hi",
		"grep foo > /tmp/f":     "grep foo",
		"sort < in.txt":         "sort",
	}
	for in, want := range cases {
		if got := StripRedirections(in); got != want {
			t.Errorf("StripRedirections(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContainsShellExpansion(t *testing.T) {
	yes := []string{`echo "$status"`, "echo ${HOME}", "echo $(date)", "echo `pwd`", "x=$?"}
	no := []string{"git status", "echo hello", "ls -la /tmp", "head -30"}
	for _, c := range yes {
		if !ContainsShellExpansion(c) {
			t.Errorf("expected expansion in %q", c)
		}
	}
	for _, c := range no {
		if ContainsShellExpansion(c) {
			t.Errorf("did not expect expansion in %q", c)
		}
	}
}

func TestShellFieldsKeepsQuotedSpans(t *testing.T) {
	got := shellFields(`echo "---EXIT: done---" tail`)
	want := []string{"echo", `"---EXIT: done---"`, "tail"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shellFields = %q, want %q", got, want)
	}
}
