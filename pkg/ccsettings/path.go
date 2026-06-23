package ccsettings

import (
	"path"
	"path/filepath"
	"strings"
)

// resolveReadEditAnchor turns a Read/Edit permission specifier into an absolute,
// `**`-aware glob, following the four gitignore-style anchors:
//
//	//path        absolute path from filesystem root   (//Users/alice/secrets/**)
//	~/path        relative to home directory           (~/Documents/*.pdf)
//	/path         relative to project root             (/src/**/*.ts)
//	path | ./path relative to current directory        (*.env, ./foo)
//
// A specifier with no path separator follows gitignore semantics and matches at
// any depth, so Read(.env) is equivalent to Read(**/.env).
func resolveReadEditAnchor(spec string, ctx ResolveContext) string {
	switch {
	case strings.HasPrefix(spec, "//"):
		// Absolute: drop one leading slash so "//Users/x" -> "/Users/x".
		return spec[1:]
	case strings.HasPrefix(spec, "~/"):
		return joinGlob(ctx.HomeDir, spec[2:])
	case strings.HasPrefix(spec, "/"):
		// Project-root relative; spec already carries its leading slash.
		return joinGlob(ctx.ProjectRoot, strings.TrimPrefix(spec, "/"))
	default:
		p := strings.TrimPrefix(spec, "./")
		if strings.Contains(p, "/") {
			// Anchored at the current directory.
			return joinGlob(ctx.CWD, p)
		}
		// Bare name: match at any depth (gitignore no-slash semantics).
		return joinGlob(ctx.CWD, "**/"+p)
	}
}

// joinGlob joins a base directory and a (possibly glob-bearing) relative pattern
// with a single slash, without cleaning away the glob metacharacters.
func joinGlob(base, rel string) string {
	base = strings.TrimRight(filepath.ToSlash(base), "/")
	rel = strings.TrimLeft(rel, "/")
	if base == "" {
		return "/" + rel
	}
	return base + "/" + rel
}

// matchPathGlob reports whether an absolute `**`-aware glob matches a candidate
// path. The candidate is resolved to absolute against ctx.CWD (and ~ expanded)
// first.
func matchPathGlob(glob, candidate string, ctx ResolveContext) bool {
	cand := absCandidate(candidate, ctx)
	globSegs := strings.Split(filepath.ToSlash(glob), "/")
	pathSegs := strings.Split(cand, "/")
	return matchSegments(globSegs, pathSegs)
}

// absCandidate normalizes a tool-call path to an absolute, slash-separated,
// cleaned path.
func absCandidate(p string, ctx ResolveContext) string {
	p = filepath.ToSlash(p)
	switch {
	case strings.HasPrefix(p, "~/"):
		p = joinGlob(ctx.HomeDir, p[2:])
	case !strings.HasPrefix(p, "/"):
		p = joinGlob(ctx.CWD, p)
	}
	return path.Clean(p)
}

// matchSegments matches a slash-split glob against slash-split path segments,
// where "**" matches zero or more whole segments and a single-segment glob
// ("*", literals, "?") matches within one segment only.
func matchSegments(globSegs, pathSegs []string) bool {
	for len(globSegs) > 0 {
		g := globSegs[0]
		if g == "**" {
			// Collapse consecutive "**".
			rest := globSegs[1:]
			if len(rest) == 0 {
				return true // trailing ** matches everything remaining
			}
			for i := 0; i <= len(pathSegs); i++ {
				if matchSegments(rest, pathSegs[i:]) {
					return true
				}
			}
			return false
		}
		if len(pathSegs) == 0 {
			return false
		}
		if ok, _ := path.Match(g, pathSegs[0]); !ok {
			return false
		}
		globSegs = globSegs[1:]
		pathSegs = pathSegs[1:]
	}
	return len(pathSegs) == 0
}
