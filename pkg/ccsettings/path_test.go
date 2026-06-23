package ccsettings

import "testing"

func ctxFixture() ResolveContext {
	return ResolveContext{
		CWD:         "/work/proj",
		ProjectRoot: "/work/proj",
		HomeDir:     "/home/alice",
	}
}

func TestResolveReadEditAnchor(t *testing.T) {
	ctx := ctxFixture()
	tests := []struct {
		spec string
		want string
	}{
		{"//Users/alice/secrets/**", "/Users/alice/secrets/**"},
		{"~/Documents/*.pdf", "/home/alice/Documents/*.pdf"},
		{"/src/**/*.ts", "/work/proj/src/**/*.ts"},
		{"*.env", "/work/proj/**/*.env"},
		{".env", "/work/proj/**/.env"},
		{"**/.env", "/work/proj/**/.env"},
		{"src/**", "/work/proj/src/**"},
		{"./foo/bar", "/work/proj/foo/bar"},
	}
	for _, tt := range tests {
		if got := resolveReadEditAnchor(tt.spec, ctx); got != tt.want {
			t.Errorf("resolveReadEditAnchor(%q) = %q, want %q", tt.spec, got, tt.want)
		}
	}
}

func TestMatchPathGlob(t *testing.T) {
	ctx := ctxFixture()
	tests := []struct {
		name      string
		spec      string
		candidate string
		want      bool
	}{
		// Read(.env) ≡ Read(**/.env): any .env at or under cwd.
		{"dotenv at root", ".env", "/work/proj/.env", true},
		{"dotenv nested", ".env", "/work/proj/src/.env", true},
		{"dotenv doublestar nested", "**/.env", "/work/proj/a/b/.env", true},
		{"dotenv parent not matched", ".env", "/work/.env", false},
		// Absolute anchor.
		{"absolute", "//Users/alice/secrets/**", "/Users/alice/secrets/key.txt", true},
		{"absolute miss", "//Users/alice/secrets/**", "/Users/bob/secrets/key.txt", false},
		// Filesystem-wide.
		{"fs wide", "//**/.env", "/anywhere/deep/.env", true},
		// Single-star stays within a segment.
		{"single star segment", "/src/*.ts", "/work/proj/src/a.ts", true},
		{"single star no cross dir", "/src/*.ts", "/work/proj/src/sub/a.ts", false},
		// Home anchor.
		{"home", "~/.zshrc", "/home/alice/.zshrc", true},
	}
	for _, tt := range tests {
		glob := resolveReadEditAnchor(tt.spec, ctx)
		if got := matchPathGlob(glob, tt.candidate, ctx); got != tt.want {
			t.Errorf("%s: matchPathGlob(%q [from %q], %q) = %v, want %v",
				tt.name, glob, tt.spec, tt.candidate, got, tt.want)
		}
	}
}
