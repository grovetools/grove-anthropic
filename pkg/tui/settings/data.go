// Package settings implements the Claude Code settings browser TUI
// (`grove-anthropic settings`). It renders the ccsettings package's merged,
// provenance-tracked view of every settings.json scope file over a core
// pager.Model with one page per analytical lens: discovered scope files, the
// permission rule tree, the directory/sandbox boundary, a rule×scope matrix, a
// live evaluation probe, an effective-summary view, and a job-centric command
// browser.
//
// Most pages are read-only — the browser observes and explains the resolved
// configuration. The Permissions and Sandbox pages additionally support
// scope-targeted edits (toggle a rule's tier, add/remove a directory or domain,
// flip a sandbox flag), and the Commands page closes the generalize→allow loop:
// it reads a job's recorded commands.jsonl, shows the live verdict for each
// command, and lets the user synthesize an allow rule of a chosen breadth from a
// command an agent actually ran. All edits flow through a dry-run-confirmed
// overlay and are written by the comment-preserving ccsettings writer. The
// Managed scope is never writable, and managed lockdowns render their areas
// read-only.
package settings

import (
	"os"
	"path/filepath"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// Data is the immutable snapshot the TUI renders. It bundles the discovered
// scope files, the cross-scope merge, the evaluation engine, and the two
// computed boundaries so every page reads from one consistent view.
type Data struct {
	Sources []ccsettings.SourceFile
	Merged  *ccsettings.MergedSettings
	Engine  *ccsettings.Engine
	Ctx     ccsettings.ResolveContext
	FS      ccsettings.FilesystemBoundary
	Net     ccsettings.NetworkPolicy
}

// Load discovers every settings scope rooted at the current working directory,
// merges them under Claude's precedence, and builds the evaluation engine plus
// the filesystem/network boundaries. Discovery is tolerant: missing or
// malformed files are reported on the Scopes page rather than failing the load.
func Load() (*Data, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	projectRoot := findProjectRoot(cwd)

	sources, err := ccsettings.Discover(ccsettings.DiscoverOptions{
		ProjectRoot: projectRoot,
		HomeDir:     home,
	})
	if err != nil {
		return nil, err
	}

	ctx := ccsettings.ResolveContext{
		CWD:         cwd,
		ProjectRoot: projectRoot,
		HomeDir:     home,
	}
	merged := ccsettings.Merge(sources, ccsettings.MergeOptions{Context: ctx})

	// autoAllowBashIfSandboxed defaults on when the sandbox is enabled, which
	// mirrors Claude's behavior of skipping the whole-tool Bash prompt for
	// commands that run inside the sandbox boundary.
	engine := ccsettings.NewEngine(merged, ccsettings.EngineOptions{
		SandboxAutoAllowBash: merged.SandboxEnabled.Value,
	})

	return &Data{
		Sources: sources,
		Merged:  merged,
		Engine:  engine,
		Ctx:     ctx,
		FS:      ccsettings.ComputeFilesystemBoundary(merged),
		Net:     ccsettings.ComputeNetworkPolicy(merged),
	}, nil
}

// findProjectRoot walks up from start looking for the directory that anchors
// Claude's project/local scope files — the nearest ancestor containing a
// .claude directory or a .git repository. It falls back to start when neither
// is found so discovery still has a stable anchor.
func findProjectRoot(start string) string {
	dir := start
	for {
		if isDir(filepath.Join(dir, ".claude")) || isDir(filepath.Join(dir, ".git")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
