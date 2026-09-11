package main

import (
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/suspensionstate"
	workdirutil "github.com/gastownhall/gascity/internal/workdir"
)

// addDoctorWorkDir appends a cleaned dir to dirs unless it is blank or already
// present, preserving insertion order. Doctor checks that enumerate agent work
// dirs share it so one physical directory is reported once even when several
// agents or pool slots resolve to it.
func addDoctorWorkDir(dirs *[]string, dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	dir = filepath.Clean(dir)
	for _, existing := range *dirs {
		if existing == dir {
			return
		}
	}
	*dirs = append(*dirs, dir)
}

// doctorAgentPoolSlots returns the pool slot numbers whose per-instance work
// dirs a doctor check must inspect in addition to the agent's own. It returns
// nil for agents that never expand into instances, and bounds the slot range by
// the agent's namepool, max-active, or min-active configuration so an unbounded
// pool does not produce an unbounded scan.
func doctorAgentPoolSlots(agent *config.Agent) []int {
	if agent == nil || !agent.SupportsInstanceExpansion() {
		return nil
	}
	limit := 1
	if len(agent.NamepoolNames) > 0 {
		limit = len(agent.NamepoolNames)
	} else if maxSessions := agent.EffectiveMaxActiveSessions(); maxSessions != nil {
		if *maxSessions <= 1 {
			return nil
		}
		limit = *maxSessions
	} else if minSessions := agent.EffectiveMinActiveSessions(); minSessions > 1 {
		limit = minSessions
	}
	slots := make([]int, 0, limit)
	for slot := 1; slot <= limit; slot++ {
		slots = append(slots, slot)
	}
	return slots
}

// resolveDoctorAgentWorkDir resolves one agent identity's work dir for a doctor
// scan. It returns an error when the work dir cannot be resolved or sits under a
// stale worktree, so a check skips that identity instead of reporting a path the
// agent will never open.
func resolveDoctorAgentWorkDir(cityPath string, cfg *config.City, agent *config.Agent, qualifiedName string) (string, error) {
	if agent == nil {
		return "", nil
	}
	cityName := loadedCityName(cfg, cityPath)
	var rigs []config.Rig
	if cfg != nil {
		rigs = cfg.Rigs
	}
	if strings.TrimSpace(qualifiedName) == "" {
		qualifiedName = agent.QualifiedName()
	}
	workDir, err := workdirutil.ResolveWorkDirPathStrict(cityPath, cityName, qualifiedName, *agent, rigs)
	if err != nil {
		return "", err
	}
	if err := workdirutil.ValidateAncestorWorktreesNotStale(workDir); err != nil {
		return "", err
	}
	return workDir, nil
}

// addDoctorAgentWorkDirs appends the work dirs of one configured agent — its own
// identity plus every bounded pool instance — to dirs.
func addDoctorAgentWorkDirs(dirs *[]string, cityPath string, cfg *config.City, agent *config.Agent) {
	addDoctorAgentWorkDir(dirs, cityPath, cfg, agent, agent.QualifiedName())
	for _, slot := range doctorAgentPoolSlots(agent) {
		instanceAgent, qualifiedInstance, _ := poolDesiredRequestIdentity(agent, slot)
		if qualifiedInstance == agent.QualifiedName() {
			continue
		}
		addDoctorAgentWorkDir(dirs, cityPath, cfg, instanceAgent, qualifiedInstance)
	}
}

func addDoctorAgentWorkDir(dirs *[]string, cityPath string, cfg *config.City, agent *config.Agent, qualifiedName string) {
	workDir, err := resolveDoctorAgentWorkDir(cityPath, cfg, agent, qualifiedName)
	if err != nil {
		return
	}
	addDoctorWorkDir(dirs, workDir)
}

// doctorSuspendedRigPaths returns the cleaned paths of every rig that is
// suspended for this city, which agentInSuspendedRig uses to drop agents whose
// rig will not be started.
func doctorSuspendedRigPaths(suspState suspensionstate.State, rigs []config.Rig) map[string]bool {
	suspended := map[string]bool{}
	for i := range rigs {
		rig := &rigs[i]
		path := strings.TrimSpace(rig.Path)
		if path == "" {
			continue
		}
		if suspensionstate.EffectiveRigSuspended(suspState, rig.Name, rig.EffectiveSuspendedOnStart()) {
			suspended[filepath.Clean(path)] = true
		}
	}
	return suspended
}
