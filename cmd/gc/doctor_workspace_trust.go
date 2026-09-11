package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/runtime"
	workdirutil "github.com/gastownhall/gascity/internal/workdir"
)

// claudeProviderFamily is the built-in provider family whose workspace trust is
// persisted per project path in the Claude Code state file. Agents resolving to
// this family (directly or through a wrapped custom provider) are the ones this
// check can verify; other families persist startup trust differently or not at
// all, so they are out of scope rather than silently assumed healthy.
const claudeProviderFamily = "claude"

// workspaceTrustCheck reports repository roots that Claude-family agents will
// open but for which Claude Code has no recorded workspace-trust acceptance.
//
// Trust is keyed by the git repository root of the directory a session opens,
// not by the worktree path, so every agent worktree of one rig shares a single
// trust entry. A root with no entry makes each new session depend on the
// runtime navigating the trust prompt at startup; when that navigation does not
// apply, the pane takes the prompt's default and dies seconds into startup. The
// pool then never reaches its minimum, routed work never dispatches, and the rig
// reads as idle rather than broken — the failure this check exists to name.
type workspaceTrustCheck struct {
	// dirs are the directories whose repository roots must carry recorded
	// trust: see claudeWorkspaceTrustDirs.
	dirs []string
	// repoRootFn resolves a work dir to its git repository root. Injected so
	// tests do not need real repositories.
	repoRootFn func(dir string) string
	// trustedFn returns the set of project roots Claude Code records trust for.
	trustedFn func() (map[string]bool, error)
}

func newWorkspaceTrustCheck(cityPath string, cfg *config.City) *workspaceTrustCheck {
	return &workspaceTrustCheck{
		dirs: claudeWorkspaceTrustDirs(cityPath, cfg),
		repoRootFn: func(dir string) string {
			return runtime.WorkspaceImportTrustRoot(context.Background(), dir)
		},
		trustedFn: readClaudeTrustedProjectRoots,
	}
}

func (c *workspaceTrustCheck) Name() string { return "workspace-trust-provisioned" }

// CanFix returns false. Clearing this warning means recording trust for a
// project in the user's own Claude Code state file, which Claude Code itself
// rewrites while sessions run; a doctor fix would race those writes over a file
// holding far more than trust. Detection only — the operator decides.
func (c *workspaceTrustCheck) CanFix() bool { return false }

// Fix is a no-op. See CanFix.
func (c *workspaceTrustCheck) Fix(_ *doctor.CheckContext) error { return nil }

// Run resolves each directory from claudeWorkspaceTrustDirs to the repository
// root Claude Code keys trust on, and reports the roots with no recorded
// acceptance. A failure to read the state file is reported alongside whatever was
// read rather than being treated as "everything is trusted".
func (c *workspaceTrustCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	if len(c.dirs) == 0 {
		return okCheck(c.Name(), "no active Claude-family workspaces to verify")
	}

	trusted, readErr := c.trustedFn()

	roots := make([]string, 0, len(c.dirs))
	dirsByRoot := make(map[string][]string, len(c.dirs))
	for _, dir := range c.dirs {
		root := c.workspaceTrustRoot(dir)
		if root == "" {
			continue
		}
		if _, seen := dirsByRoot[root]; !seen {
			roots = append(roots, root)
		}
		dirsByRoot[root] = append(dirsByRoot[root], dir)
	}
	sort.Strings(roots)

	var untrusted []string
	for _, root := range roots {
		if !trusted[root] {
			untrusted = append(untrusted, fmt.Sprintf("%s (opened by %s)", root, strings.Join(dirsByRoot[root], ", ")))
		}
	}

	if readErr != nil {
		details := append([]string{fmt.Sprintf("Claude state unreadable: %v", readErr)}, untrusted...)
		return warnCheck(c.Name(),
			"could not confirm recorded workspace trust for every Claude-family workspace root",
			"fix access to the Claude Code state file, then rerun gc doctor",
			details)
	}
	if len(untrusted) == 0 {
		return okCheck(c.Name(), fmt.Sprintf("%d workspace root(s) have recorded Claude trust", len(roots)))
	}
	return warnCheck(c.Name(),
		fmt.Sprintf("%d of %d workspace root(s) have no recorded Claude trust acceptance", len(untrusted), len(roots)),
		"open each root once interactively (run the provider CLI there and accept the workspace-trust prompt) so new sessions never depend on the startup prompt; a root left untrusted can leave the pool permanently empty",
		untrusted)
}

// workspaceTrustRoot resolves dir to the path Claude Code keys trust on: the
// repository root when dir is inside a git repository, and dir itself when it is
// not, since a non-repository directory is trusted under its own path.
func (c *workspaceTrustCheck) workspaceTrustRoot(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if c.repoRootFn != nil {
		if root := strings.TrimSpace(c.repoRootFn(dir)); root != "" {
			return filepath.Clean(root)
		}
	}
	return filepath.Clean(dir)
}

// readClaudeTrustedProjectRoots reads the recorded trust set from the Claude
// state files for the invoking user's environment.
func readClaudeTrustedProjectRoots() (map[string]bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	paths := runtime.ClaudeStateFilePaths(home, strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")))
	if len(paths) == 0 {
		// Reporting every root as untrusted here would blame the rigs for an
		// environment problem, so name the real cause instead.
		return nil, fmt.Errorf("no Claude state location resolvable: set HOME or CLAUDE_CONFIG_DIR: %w", err)
	}
	return runtime.ClaudeTrustedProjectRoots(paths)
}

// claudeWorkspaceTrustDirs returns the directories whose repository roots must
// carry recorded Claude trust for this city: the rig (or city) root every active
// Claude-family agent is bound to, plus each such agent's own work dir when that
// directory already exists.
//
// A work dir that has not been created yet is deliberately skipped. Its
// repository root cannot be resolved from disk, and guessing one would report a
// separate untrusted root for every unstarted pool slot while the rig root those
// worktrees will actually resolve to is already covered. Resolving existing work
// dirs is still worth doing: it catches a work dir whose repository root differs
// from its rig's, which a shared trust entry would not cover.
//
// Suspended agents, and agents in a suspended rig, are excluded because they are
// not started and so cannot hit a trust prompt.
func claudeWorkspaceTrustDirs(cityPath string, cfg *config.City) []string {
	if cfg == nil {
		return nil
	}
	suspState, _ := loadSuspensionState(fsys.OSFS{}, cityPath)
	suspendedRigPaths := doctorSuspendedRigPaths(suspState, cfg.Rigs)
	rigPaths := make(map[string]string, len(cfg.Rigs))
	for i := range cfg.Rigs {
		rigPaths[cfg.Rigs[i].Name] = strings.TrimSpace(cfg.Rigs[i].Path)
	}

	var dirs []string
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if agent.Suspended || agentInSuspendedRig(cityPath, agent, cfg.Rigs, suspendedRigPaths) {
			continue
		}
		if effectiveAgentProviderFamily(agent, cfg.Workspace.Provider, cfg.Providers) != claudeProviderFamily {
			continue
		}
		if rigName := workdirutil.ConfiguredRigName(cityPath, *agent, cfg.Rigs); rigName != "" {
			addDoctorWorkDir(&dirs, rigPaths[rigName])
		} else {
			addDoctorWorkDir(&dirs, cityPath)
		}
		var agentDirs []string
		addDoctorAgentWorkDirs(&agentDirs, cityPath, cfg, agent)
		for _, dir := range agentDirs {
			if isExistingDir(dir) {
				addDoctorWorkDir(&dirs, dir)
			}
		}
	}
	sort.Strings(dirs)
	return dirs
}
