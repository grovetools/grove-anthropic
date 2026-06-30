package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/claudenotebook"
	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"
	"github.com/spf13/cobra"
)

// newSyncSettingsCmd builds the `grove-anthropic sync-settings` command, which
// propagates the [claude] grove.toml profile (permissions.allow +
// sandbox{enabled, failIfUnavailable, autoAllowBashIfSandboxed,
// filesystem.allowWrite, network.allowedDomains}) into each workspace/worktree's
// .claude/settings.local.json.
//
// It mirrors the manual skills-sync command (skills/cmd/skills.go
// newSkillsSyncCmd): it calls the generalized seeder IN-PROCESS (the daemon has
// no generic reconcile RPC). Targets are enumerated authoritatively via
// workspace.DiscoveryService.DiscoverAll (which walks ALL workspaces AND all
// worktrees, including ecosystem-level worktrees) plus worktreeregistry.ListAll
// (for per-worktree member-repo resolution). For each target worktree it
// resolves the member repos and calls workspace.SeedClaudeSettingsForWorktree,
// which unions each member repo's [claude] block and seeds the result
// additively (never clobbering user edits).
func newSyncSettingsCmd() *cobra.Command {
	var all, allWorkspaces, ecosystem, dryRun bool
	var workspaceName string

	cmd := &cobra.Command{
		Use:   "sync-settings",
		Short: "Propagate the [claude] grove.toml profile to .claude/settings.local.json",
		Long: `Propagate the [claude] grove.toml profile into each workspace/worktree's
.claude/settings.local.json.

This reads the [claude] block from grove.toml and seeds the declared
permissions and sandbox settings into the target worktrees, additively merging
with any existing (and user-added) entries. For ecosystem worktrees the
[claude] blocks of all member repos are unioned.

Example grove.toml configuration:

  [claude.permissions]
  allow = ["Bash(git:*)"]

  [claude.sandbox]
  enabled = true

  [claude.sandbox.filesystem]
  allowWrite = ["/tmp/myproject"]

  [claude.sandbox.network]
  allowedDomains = ["api.github.com"]

By default every discovered workspace and worktree is targeted. Use
--workspace to limit to a single workspace (and its worktrees), --ecosystem
to limit to workspaces in the current ecosystem, and --dry-run to print the
additions that WOULD be written without touching any file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := logging.NewPrettyLogger()
			return runSyncSettings(logger, all || allWorkspaces, ecosystem, workspaceName, dryRun)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Sync settings for all workspaces and worktrees (default behavior).")
	cmd.Flags().BoolVar(&allWorkspaces, "all-workspaces", false, "Alias for --all: sync settings for all registered workspaces.")
	cmd.Flags().BoolVar(&ecosystem, "ecosystem", false, "Sync settings for all workspaces in the current ecosystem.")
	cmd.Flags().StringVar(&workspaceName, "workspace", "", "Limit syncing to a single workspace (matched by name) and its worktrees.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the additions that would be written without modifying any file.")

	return cmd
}

// syncTarget is a single worktree to seed plus its resolved member repos.
type syncTarget struct {
	path  string   // absolute worktree path
	repos []string // member-repo subdir names (empty for single-repo worktrees)
}

func runSyncSettings(logger *logging.PrettyLogger, all bool, ecosystem bool, workspaceName string, dryRun bool) error {
	// Reconcile the XDG worktree registry first so enumeration sees freshly
	// created worktrees (mirrors the daemon collector pattern).
	if err := worktreeregistry.Reconcile(paths.WorktreesDir()); err != nil {
		logger.WarnPretty(fmt.Sprintf("worktree registry reconcile failed (continuing): %v", err))
	}

	// Authoritative enumeration of ALL workspaces + worktrees, including
	// ecosystem-level worktrees (DiscoverAll explicitly walks XDG ecosystem
	// worktrees that the in-tree directory walk can never reach).
	disc := workspace.NewDiscoveryService(nil)
	result, err := disc.DiscoverAll()
	if err != nil {
		return fmt.Errorf("discover workspaces: %w", err)
	}

	// Build a provider so the notebook-dir resolver can use the in-memory index.
	provider := workspace.NewProvider(result)

	// For --ecosystem filtering, determine the current ecosystem path.
	var currentEcoPath string
	if ecosystem {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("could not get current directory: %w", err)
		}
		currentNode, err := workspace.GetProjectByPath(cwd)
		if err != nil {
			return fmt.Errorf("--ecosystem requires being in a workspace: %w", err)
		}
		currentEcoPath = currentNode.RootEcosystemPath
		if currentEcoPath == "" {
			// If current node IS the ecosystem root, use its path.
			if currentNode.Kind == workspace.KindEcosystemRoot || currentNode.Kind == workspace.KindEcosystemWorktree {
				currentEcoPath = currentNode.Path
			} else {
				return fmt.Errorf("current directory is not part of an ecosystem")
			}
		}
	}

	// Map worktree path -> member repos from the registry (authoritative for
	// ecosystem worktrees, whose member set the filesystem layout alone can't
	// encode).
	reposByPath := map[string][]string{}
	entries, listErr := worktreeregistry.ListAll()
	if listErr != nil {
		logger.WarnPretty(fmt.Sprintf("list worktree registry failed (continuing): %v", listErr))
	}
	for _, e := range entries {
		if e == nil || e.AbsPath == "" {
			continue
		}
		reposByPath[normalizePath(e.AbsPath)] = e.Repos
	}

	// Collect target worktrees from discovery, filtered by --workspace or --ecosystem.
	seen := map[string]bool{}
	var targets []syncTarget
	addTarget := func(path string) {
		if path == "" {
			return
		}
		key := normalizePath(path)
		if seen[key] {
			return
		}
		seen[key] = true
		repos := reposByPath[key]
		targets = append(targets, syncTarget{path: path, repos: repos})
		claudenotebook.Debugf("sync-settings target ADDED path=%s repos=%v", path, repos)
	}

	for _, proj := range result.Projects {
		if workspaceName != "" && proj.Name != workspaceName {
			continue
		}
		// Filter by ecosystem if --ecosystem was provided.
		if currentEcoPath != "" && proj.ParentEcosystemPath != currentEcoPath && proj.Path != currentEcoPath {
			continue
		}
		// The project root itself is a workspace (primary checkout).
		addTarget(proj.Path)
		for _, ws := range proj.Workspaces {
			if workspaceName != "" && proj.Name != workspaceName && ws.Name != workspaceName {
				continue
			}
			addTarget(ws.Path)
		}
	}

	// The ecosystem ROOT / primary checkout (e.g. /Users/.../Code/grovetools) is
	// itself a seedable target: an agent launched there reads its own
	// .claude/settings.local.json. DiscoverAll surfaces it under result.Ecosystems
	// — NOT result.Projects — so the loop above (which only walks Projects) never
	// adds it, and it never had a registry entry either. Enumerate ecosystems
	// explicitly, resolving each one's member repos from the sub-projects grouped
	// beneath it so the root gets the SAME member union an XDG ecosystem worktree
	// gets (member [claude] blocks + paired notebook dirs).
	ecoReposByPath := map[string][]string{}
	for _, proj := range result.Projects {
		if proj.ParentEcosystemPath == "" || proj.Path == "" {
			continue
		}
		ek := normalizePath(proj.ParentEcosystemPath)
		ecoReposByPath[ek] = append(ecoReposByPath[ek], filepath.Base(proj.Path))
	}
	for _, eco := range result.Ecosystems {
		if eco.Path == "" {
			continue
		}
		if workspaceName != "" && eco.Name != workspaceName {
			continue
		}
		if currentEcoPath != "" && eco.Path != currentEcoPath {
			continue
		}
		key := normalizePath(eco.Path)
		// Prefer a registry-provided member set if one exists (ecosystem worktrees
		// can also appear here); otherwise fall back to the discovered subdirs.
		if _, ok := reposByPath[key]; !ok {
			reposByPath[key] = ecoReposByPath[key]
		}
		claudenotebook.Debugf("sync-settings ecosystem ROOT included: name=%s path=%s repos=%v",
			eco.Name, eco.Path, reposByPath[key])
		addTarget(eco.Path)
	}

	// Also include any registry worktrees discovery did not surface (e.g.
	// zombie / out-of-grove worktrees). Skip when limiting to one workspace or ecosystem.
	if workspaceName == "" && currentEcoPath == "" {
		for _, e := range entries {
			if e == nil || e.AbsPath == "" {
				continue
			}
			addTarget(e.AbsPath)
		}
	}

	if len(targets) == 0 {
		if workspaceName != "" {
			logger.InfoPretty(fmt.Sprintf("No workspace matched %q.", workspaceName))
		} else {
			logger.InfoPretty("No workspaces or worktrees discovered.")
		}
		return nil
	}

	sort.Slice(targets, func(i, j int) bool { return targets[i].path < targets[j].path })

	mode := "applied"
	if dryRun {
		mode = "dry-run"
	}
	logger.InfoPretty(fmt.Sprintf("sync-settings: %d target worktree(s) [%s]", len(targets), mode))

	var failures int
	for _, t := range targets {
		if dryRun {
			if err := reportDryRun(logger, t); err != nil {
				logger.ErrorPretty(t.path, err)
				failures++
			}
			continue
		}
		if err := workspace.SeedClaudeSettingsForWorktree(t.path, t.repos, provider); err != nil {
			logger.ErrorPretty(t.path, err)
			failures++
			continue
		}
		logger.Success(fmt.Sprintf("%s (applied)", t.path))
	}

	if failures > 0 {
		return fmt.Errorf("sync-settings: %d of %d target(s) failed", failures, len(targets))
	}
	return nil
}

// reportDryRun resolves the [claude] profile for a target worktree (the same
// union the seeder performs) and prints the additions that WOULD be written to
// its settings.local.json, WITHOUT modifying any file.
func reportDryRun(logger *logging.PrettyLogger, t syncTarget) error {
	cfg := resolveWorktreeClaudeConfig(t.path, t.repos)
	settingsPath := filepath.Join(t.path, ".claude", "settings.local.json")

	if cfg == nil || !cfg.ShouldSeed() {
		logger.InfoPretty(fmt.Sprintf("%s (dry-run): no [claude] profile resolved -> %s", t.path, settingsPath))
		return nil
	}

	existing, err := readSettings(settingsPath)
	if err != nil {
		return err
	}

	var additions []string
	for _, a := range []struct {
		path   []string
		values []string
	}{
		{[]string{"permissions", "allow"}, cfg.Permissions.Allow},
		{[]string{"permissions", "deny"}, cfg.Permissions.Deny},
		{[]string{"sandbox", "filesystem", "allowWrite"}, cfg.Sandbox.Filesystem.AllowWrite},
		{[]string{"sandbox", "filesystem", "denyWrite"}, cfg.Sandbox.Filesystem.DenyWrite},
		{[]string{"sandbox", "network", "allowedDomains"}, cfg.Sandbox.Network.AllowedDomains},
	} {
		present := existingStringSet(existing, a.path)
		for _, v := range a.values {
			if _, ok := present[v]; !ok {
				additions = append(additions, fmt.Sprintf("+ %s += %q", joinPath(a.path), v))
			}
		}
	}

	for _, b := range []struct {
		path []string
		val  *bool
	}{
		{[]string{"sandbox", "enabled"}, cfg.Sandbox.Enabled},
		{[]string{"sandbox", "failIfUnavailable"}, cfg.Sandbox.FailIfUnavailable},
		{[]string{"sandbox", "autoAllowBashIfSandboxed"}, cfg.Sandbox.AutoAllowBashIfSandboxed},
	} {
		if b.val == nil {
			continue
		}
		cur, has := existingBool(existing, b.path)
		if !has || cur != *b.val {
			additions = append(additions, fmt.Sprintf("~ %s = %v", joinPath(b.path), *b.val))
		}
	}

	if len(additions) == 0 {
		logger.InfoPretty(fmt.Sprintf("%s (dry-run): already up to date -> %s", t.path, settingsPath))
		return nil
	}

	logger.InfoPretty(fmt.Sprintf("%s (dry-run): %d change(s) to %s", t.path, len(additions), settingsPath))
	for _, line := range additions {
		logger.InfoPretty("    " + line)
	}
	return nil
}

// resolveWorktreeClaudeConfig mirrors the config-union half of
// workspace.SeedClaudeSettingsForWorktree (without writing or resolving
// notebook dirs), so --dry-run can show exactly which [claude] entries the
// apply path would merge. Returns nil if nothing resolves.
func resolveWorktreeClaudeConfig(worktreePath string, repos []string) *claudenotebook.ClaudeConfig {
	var merged claudenotebook.ClaudeConfig
	if rootCfg, _ := config.LoadFrom(worktreePath); rootCfg != nil {
		_ = rootCfg.UnmarshalExtension("claude", &merged)
	}
	for _, repo := range repos {
		if repo == "" {
			continue
		}
		repoCfg, err := config.LoadFrom(filepath.Join(worktreePath, repo))
		if err != nil || repoCfg == nil {
			continue
		}
		var memberCfg claudenotebook.ClaudeConfig
		if err := repoCfg.UnmarshalExtension("claude", &memberCfg); err != nil {
			continue
		}
		merged.Merge(&memberCfg)
	}
	// Gate on ShouldSeed, not bare IsEmpty: a protectConfig-only (or
	// allowGroveTools-only) profile is IsEmpty()==true yet still seeds, so
	// returning nil here would make --dry-run wrongly report "no profile".
	if !merged.ShouldSeed() {
		return nil
	}
	return &merged
}

// readSettings parses an existing settings.local.json into a generic map. A
// missing file yields an empty map; a malformed file is a hard error (the seeder
// would also refuse to clobber it).
func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	root := map[string]any{}
	if len(data) == 0 {
		return root, nil
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return root, nil
}

// existingStringSet returns the set of string entries already present at the
// given nested array path.
func existingStringSet(root map[string]any, path []string) map[string]struct{} {
	out := map[string]struct{}{}
	node := descend(root, path[:len(path)-1])
	if node == nil {
		return out
	}
	if raw, ok := node[path[len(path)-1]].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				out[s] = struct{}{}
			}
		}
	}
	return out
}

// existingBool returns the bool at the given nested path and whether it exists.
func existingBool(root map[string]any, path []string) (bool, bool) {
	node := descend(root, path[:len(path)-1])
	if node == nil {
		return false, false
	}
	if b, ok := node[path[len(path)-1]].(bool); ok {
		return b, true
	}
	return false, false
}

// descend walks the nested object path, returning nil if any segment is absent
// or not an object.
func descend(root map[string]any, path []string) map[string]any {
	node := root
	for _, key := range path {
		child, ok := node[key].(map[string]any)
		if !ok {
			return nil
		}
		node = child
	}
	return node
}

func joinPath(path []string) string {
	out := ""
	for i, p := range path {
		if i > 0 {
			out += "."
		}
		out += p
	}
	return out
}

func normalizePath(path string) string {
	if n, err := pathutil.NormalizeForLookup(path); err == nil {
		return n
	}
	return path
}
