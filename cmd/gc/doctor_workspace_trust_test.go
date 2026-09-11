package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

var errTestTrustStateUnreadable = errors.New("reading Claude state /home/u/.claude.json: permission denied")

func newTestWorkspaceTrustCheck(workDirs []string, roots map[string]string, trusted map[string]bool) *workspaceTrustCheck {
	return &workspaceTrustCheck{
		dirs: workDirs,
		repoRootFn: func(dir string) string {
			return roots[dir]
		},
		trustedFn: func() (map[string]bool, error) {
			return trusted, nil
		},
	}
}

func TestWorkspaceTrustCheckNameAndFixPolicy(t *testing.T) {
	check := &workspaceTrustCheck{}
	if got, want := check.Name(), "workspace-trust-provisioned"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if check.CanFix() {
		t.Error("CanFix() = true; the check must never write the user's Claude state file")
	}
	if err := check.Fix(&doctor.CheckContext{}); err != nil {
		t.Errorf("Fix() = %v, want nil no-op", err)
	}
	if check.WarmupEligible() {
		t.Error("WarmupEligible() = true, want false")
	}
}

func TestWorkspaceTrustCheckOKWhenEveryRootIsTrusted(t *testing.T) {
	check := newTestWorkspaceTrustCheck(
		[]string{"/city/.gc/worktrees/rig/one", "/city/.gc/worktrees/rig/two"},
		map[string]string{
			"/city/.gc/worktrees/rig/one": "/repo",
			"/city/.gc/worktrees/rig/two": "/repo",
		},
		map[string]bool{"/repo": true},
	)

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want OK (message %q, details %#v)", res.Status, res.Message, res.Details)
	}
	if !strings.Contains(res.Message, "1 workspace root") {
		t.Errorf("message = %q, want the deduped root count", res.Message)
	}
}

func TestWorkspaceTrustCheckWarnsAndNamesUntrustedRoots(t *testing.T) {
	check := newTestWorkspaceTrustCheck(
		[]string{"/city/.gc/worktrees/new/one", "/city/.gc/worktrees/old/one"},
		map[string]string{
			"/city/.gc/worktrees/new/one": "/repo/fresh",
			"/city/.gc/worktrees/old/one": "/repo/known",
		},
		map[string]bool{"/repo/known": true},
	)

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want warning", res.Status)
	}
	joined := strings.Join(res.Details, "\n")
	if !strings.Contains(joined, "/repo/fresh") {
		t.Errorf("details %#v must name the untrusted root", res.Details)
	}
	if strings.Contains(joined, "/repo/known") {
		t.Errorf("details %#v must not name the trusted root", res.Details)
	}
	if !strings.Contains(joined, "/city/.gc/worktrees/new/one") {
		t.Errorf("details %#v must name a work dir that resolves to the untrusted root", res.Details)
	}
	if strings.TrimSpace(res.FixHint) == "" {
		t.Error("FixHint must tell the operator how to clear the warning")
	}
}

func TestWorkspaceTrustCheckOKWhenNoClaudeFamilyWorkspaces(t *testing.T) {
	check := newTestWorkspaceTrustCheck(nil, nil, nil)

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want OK", res.Status)
	}
}

func TestWorkspaceTrustCheckFallsBackToWorkDirWhenRepoRootUnresolvable(t *testing.T) {
	check := newTestWorkspaceTrustCheck(
		[]string{"/outside/not-a-repo"},
		map[string]string{},
		map[string]bool{},
	)

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want warning", res.Status)
	}
	if !strings.Contains(strings.Join(res.Details, "\n"), "/outside/not-a-repo") {
		t.Errorf("details %#v must fall back to the work dir itself", res.Details)
	}
}

func TestWorkspaceTrustCheckWarnsWhenTrustStateUnreadable(t *testing.T) {
	check := &workspaceTrustCheck{
		dirs:       []string{"/city/.gc/worktrees/rig/one"},
		repoRootFn: func(string) string { return "/repo" },
		trustedFn: func() (map[string]bool, error) {
			return map[string]bool{"/repo": true}, errTestTrustStateUnreadable
		},
	}

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want warning so the read failure is not swallowed", res.Status)
	}
	if !strings.Contains(strings.Join(res.Details, "\n"), errTestTrustStateUnreadable.Error()) {
		t.Errorf("details %#v must surface the read failure", res.Details)
	}
}

func TestClaudeWorkspaceTrustDirsSelectsOnlyActiveClaudeFamilyAgents(t *testing.T) {
	cityDir := t.TempDir()
	activeRig := filepath.Join(cityDir, "rigs", "active")
	suspendedRig := filepath.Join(cityDir, "rigs", "suspended")
	wrappedBase := "builtin:claude"
	inheritsDir := filepath.Join(cityDir, ".gc", "agents", "inherits")
	wrappedDir := filepath.Join(cityDir, ".gc", "agents", "wrapped")
	codexDir := filepath.Join(cityDir, ".gc", "agents", "codexer")
	parkedDir := filepath.Join(cityDir, ".gc", "agents", "parked")
	inactiveRigDir := filepath.Join(cityDir, ".gc", "agents", "inactive-rig")
	for _, dir := range []string{activeRig, suspendedRig, inheritsDir, wrappedDir, codexDir, parkedDir, inactiveRigDir} {
		mkdirAllForTest(t, dir)
	}
	cfg := &config.City{
		Workspace: config.Workspace{Provider: "claude"},
		Providers: map[string]config.ProviderSpec{
			"fast-claude": {Base: &wrappedBase},
		},
		Rigs: []config.Rig{
			{Name: "active", Path: activeRig},
			{Name: "suspended", Path: suspendedRig, SuspendedOnStart: true},
		},
		Agents: []config.Agent{
			{Name: "inherits", Dir: "active", WorkDir: inheritsDir},
			{Name: "wrapped", Dir: "active", Provider: "fast-claude", WorkDir: wrappedDir},
			{Name: "codexer", Dir: "active", Provider: "codex", WorkDir: codexDir},
			{Name: "parked", Dir: "active", Suspended: true, WorkDir: parkedDir},
			{Name: "inactive-rig", Dir: "suspended", WorkDir: inactiveRigDir},
		},
	}

	got := claudeWorkspaceTrustDirs(cityDir, cfg)

	assertDoctorPathPresent(t, got, activeRig)
	assertDoctorPathPresent(t, got, inheritsDir)
	assertDoctorPathPresent(t, got, wrappedDir)
	assertDoctorPathAbsent(t, got, codexDir)
	assertDoctorPathAbsent(t, got, parkedDir)
	assertDoctorPathAbsent(t, got, inactiveRigDir)
	assertDoctorPathAbsent(t, got, suspendedRig)
}

func TestClaudeWorkspaceTrustDirsIncludesExistingPoolInstanceWorkDirs(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := filepath.Join(cityDir, "rigs", "active")
	slotOne := filepath.Join(cityDir, ".gc", "worktrees", "active", "worker-1")
	mkdirAllForTest(t, rigDir)
	mkdirAllForTest(t, slotOne)
	maxSessions := 2
	cfg := &config.City{
		Workspace: config.Workspace{Provider: "claude"},
		Rigs:      []config.Rig{{Name: "active", Path: rigDir}},
		Agents: []config.Agent{{
			Name:              "worker",
			Dir:               "active",
			WorkDir:           filepath.Join(".gc", "worktrees", "{{.Rig}}", "{{.AgentBase}}"),
			MaxActiveSessions: &maxSessions,
		}},
	}

	got := claudeWorkspaceTrustDirs(cityDir, cfg)

	assertDoctorPathPresent(t, got, slotOne)
	// worker-2 has never been created, so it contributes no root of its own;
	// the rig root every pool worktree resolves to already covers it. Guessing
	// a root per unstarted slot is what made this check unreadable noise.
	assertDoctorPathAbsent(t, got, filepath.Join(cityDir, ".gc", "worktrees", "active", "worker-2"))
	assertDoctorPathPresent(t, got, rigDir)
}

func TestClaudeWorkspaceTrustDirsFallsBackToCityForUnscopedAgents(t *testing.T) {
	cityDir := t.TempDir()
	cfg := &config.City{
		Workspace: config.Workspace{Provider: "claude"},
		Agents:    []config.Agent{{Name: "overseer", Scope: "city"}},
	}

	got := claudeWorkspaceTrustDirs(cityDir, cfg)

	assertDoctorPathPresent(t, got, cityDir)
}

func TestClaudeWorkspaceTrustDirsEmptyWithoutConfig(t *testing.T) {
	if got := claudeWorkspaceTrustDirs(t.TempDir(), nil); len(got) != 0 {
		t.Fatalf("dirs = %#v, want none", got)
	}
}

func mkdirAllForTest(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}
