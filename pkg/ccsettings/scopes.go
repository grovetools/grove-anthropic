package ccsettings

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Scope identifies one layer of Claude Code's settings precedence. Values are
// ordered by precedence, highest first, so that comparisons and sorting line up
// with Claude's rule: Managed > CLI > Local > Project > User.
//
// CLI is transient — it has no settings file on disk — but it occupies a fixed
// rung between Managed and Local, so it is represented here for the resolver.
type Scope int

const (
	ScopeManaged Scope = iota // policy/managed settings, highest precedence, read-only
	ScopeCLI                  // transient --add-dir/--allowedTools session overrides
	ScopeLocal                // .claude/settings.local.json (gitignored)
	ScopeProject              // .claude/settings.json (committed)
	ScopeUser                 // ~/.claude/settings.json
)

// String returns the internal scope key Claude uses, matching the v2.1.187
// binary's userSettings/projectSettings/localSettings/policySettings naming.
func (s Scope) String() string {
	switch s {
	case ScopeManaged:
		return "policySettings"
	case ScopeCLI:
		return "cliArgs"
	case ScopeLocal:
		return "localSettings"
	case ScopeProject:
		return "projectSettings"
	case ScopeUser:
		return "userSettings"
	default:
		return fmt.Sprintf("scope(%d)", int(s))
	}
}

// Label returns a short human label for display.
func (s Scope) Label() string {
	switch s {
	case ScopeManaged:
		return "Managed"
	case ScopeCLI:
		return "CLI"
	case ScopeLocal:
		return "Local"
	case ScopeProject:
		return "Project"
	case ScopeUser:
		return "User"
	default:
		return fmt.Sprintf("Scope(%d)", int(s))
	}
}

// fileScopesByPrecedence lists the on-disk scopes from highest precedence to
// lowest. CLI is omitted: it is injected programmatically, not discovered.
var fileScopesByPrecedence = []Scope{ScopeManaged, ScopeLocal, ScopeProject, ScopeUser}

// SourceFile is one discovered settings file (or its expected location, when
// absent), with its parsed contents and any parse error.
type SourceFile struct {
	Scope      Scope
	Path       string
	Exists     bool
	Settings   *Settings // nil when the file is absent or failed to parse
	ParseError error
	Raw        []byte
}

// DiscoverOptions parameterizes scope-file discovery. The override fields exist
// so tests can point discovery at fixture trees and exercise every OS's managed
// paths without touching the real filesystem layout.
type DiscoverOptions struct {
	// ProjectRoot anchors the Project and Local scope files. When empty, the
	// current working directory is used.
	ProjectRoot string
	// HomeDir overrides the user-settings home (defaults to os.UserHomeDir).
	HomeDir string
	// GOOS overrides runtime.GOOS for managed-path selection ("darwin",
	// "linux", "windows"). When empty, runtime.GOOS is used.
	GOOS string
	// ManagedPaths, when non-nil, replaces the per-OS managed-settings paths
	// entirely (tests pass fixture paths here). The first existing path wins.
	ManagedPaths []string
}

// managedSettingsPaths returns the candidate managed-settings file locations
// for the given OS, in the order Claude consults them.
func managedSettingsPaths(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}
	case "windows":
		return []string{`C:\Program Files\ClaudeCode\managed-settings.json`}
	default: // linux, wsl, and anything else Claude treats as Linux
		return []string{"/etc/claude-code/managed-settings.json"}
	}
}

// scopePath returns the settings file path for a file-backed scope.
func scopePath(scope Scope, opts DiscoverOptions, goos, home string) (string, bool) {
	switch scope {
	case ScopeManaged:
		paths := opts.ManagedPaths
		if paths == nil {
			paths = managedSettingsPaths(goos)
		}
		// Prefer the first existing managed file; fall back to the first
		// candidate so the absent-file location is still reported.
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
		if len(paths) > 0 {
			return paths[0], false
		}
		return "", false
	case ScopeUser:
		return filepath.Join(home, ".claude", "settings.json"), true
	case ScopeProject:
		return filepath.Join(opts.ProjectRoot, ".claude", "settings.json"), true
	case ScopeLocal:
		return filepath.Join(opts.ProjectRoot, ".claude", "settings.local.json"), true
	default:
		return "", false
	}
}

// Discover enumerates every scope file that could contribute to the merged
// settings, in precedence order (Managed first), parsing each that exists.
//
// Discovery never fails on a missing or malformed file: an absent file is
// reported with Exists=false, and a parse failure is captured in
// SourceFile.ParseError while the others continue. Only an inability to resolve
// the home directory returns an error.
func Discover(opts DiscoverOptions) ([]SourceFile, error) {
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	home := opts.HomeDir
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
		home = h
	}
	if opts.ProjectRoot == "" {
		if wd, err := os.Getwd(); err == nil {
			opts.ProjectRoot = wd
		}
	}

	var files []SourceFile
	for _, scope := range fileScopesByPrecedence {
		path, ok := scopePath(scope, opts, goos, home)
		if !ok {
			continue
		}
		files = append(files, loadSourceFile(scope, path))
	}
	return files, nil
}

// loadSourceFile reads and parses one scope file, tolerating absence and parse
// errors.
func loadSourceFile(scope Scope, path string) SourceFile {
	sf := SourceFile{Scope: scope, Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sf
		}
		sf.ParseError = fmt.Errorf("read %s: %w", path, err)
		return sf
	}
	sf.Exists = true
	sf.Raw = data
	settings, err := Parse(data)
	if err != nil {
		sf.ParseError = fmt.Errorf("parse %s: %w", path, err)
		return sf
	}
	sf.Settings = settings
	return sf
}

// Parse decodes a settings.json byte slice into the tolerant typed model. An
// empty or whitespace-only document parses to an empty Settings.
func Parse(data []byte) (*Settings, error) {
	var s Settings
	trimmed := trimSpace(data)
	if len(trimmed) == 0 {
		return &s, nil
	}
	if err := s.UnmarshalJSON(trimmed); err != nil {
		return nil, err
	}
	return &s, nil
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && isSpace(b[i]) {
		i++
	}
	for j > i && isSpace(b[j-1]) {
		j--
	}
	return b[i:j]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
