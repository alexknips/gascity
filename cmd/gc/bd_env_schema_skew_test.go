package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// writeSkewCityTOML writes a minimal city.toml whose [workspace.env] carries
// the given entries (each already formatted as a TOML key/value line).
func writeSkewCityTOML(t *testing.T, cityPath string, workspaceEnv ...string) {
	t.Helper()
	body := "[workspace]\nname = \"skew-city\"\n"
	if len(workspaceEnv) > 0 {
		body += "[workspace.env]\n" + strings.Join(workspaceEnv, "\n") + "\n"
	}
	body += "[beads]\nprovider = \"file\"\n"
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestBdRuntimeEnvCarriesWorkspaceSchemaSkewOverride pins the store-env half of
// ga-ili. A skew posture declared in city.toml must reach the projected bd
// environment: every bd subprocess gc runs for a city or rig store is layered
// onto this map, and a federated hook claim is given nothing but this map.
func TestBdRuntimeEnvCarriesWorkspaceSchemaSkewOverride(t *testing.T) {
	cityPath := t.TempDir()
	writeSkewCityTOML(t, cityPath, `BD_IGNORE_SCHEMA_SKEW = "1"`)
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")

	env := mustBdRuntimeEnv(t, cityPath)
	if got := env["BD_IGNORE_SCHEMA_SKEW"]; got != "1" {
		t.Fatalf("BD_IGNORE_SCHEMA_SKEW = %q, want workspace-configured %q", got, "1")
	}
}

// TestBdRuntimeEnvCarriesAmbientSchemaSkewOverride keeps the documented
// `BD_IGNORE_SCHEMA_SKEW=1 gc <cmd>` escape hatch working for a city that has
// not declared the posture. Inheritance alone is not enough: the exact-env
// claim runner sends only this map.
func TestBdRuntimeEnvCarriesAmbientSchemaSkewOverride(t *testing.T) {
	cityPath := t.TempDir()
	writeSkewCityTOML(t, cityPath)
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")

	env := mustBdRuntimeEnv(t, cityPath)
	if got := env["BD_IGNORE_SCHEMA_SKEW"]; got != "1" {
		t.Fatalf("BD_IGNORE_SCHEMA_SKEW = %q, want ambient %q", got, "1")
	}
}

// TestBdRuntimeEnvPrefersWorkspaceSchemaSkewOverride pins the precedence: the
// city's declared posture wins over whatever an operator's shell happens to
// export, so every managed process agrees on one answer.
func TestBdRuntimeEnvPrefersWorkspaceSchemaSkewOverride(t *testing.T) {
	cityPath := t.TempDir()
	writeSkewCityTOML(t, cityPath, `BD_IGNORE_SCHEMA_SKEW = "0"`)
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")

	env := mustBdRuntimeEnv(t, cityPath)
	if got := env["BD_IGNORE_SCHEMA_SKEW"]; got != "0" {
		t.Fatalf("BD_IGNORE_SCHEMA_SKEW = %q, want workspace-configured %q", got, "0")
	}
}

// TestBdSchemaSkewOverrideOmittedForUnconfiguredCity keeps gc out of the
// policy business. Unlike the auto-backup and routing opt-outs, gc never picks
// a value here: when neither the city nor the ambient environment sets the key,
// the projection carries no key at all, so bd keeps its own fail-closed default.
// The ambient source is injected as absent, so the assertion holds even when
// the test runner's own shell exports the override.
func TestBdSchemaSkewOverrideOmittedForUnconfiguredCity(t *testing.T) {
	cityPath := t.TempDir()
	writeSkewCityTOML(t, cityPath)
	workspace, ok, err := workspaceEnvForCity(cityPath)
	if err != nil {
		t.Fatalf("workspaceEnvForCity: %v", err)
	}
	if !ok {
		t.Fatal("workspaceEnvForCity found no city.toml, want the written unconfigured city")
	}

	env := map[string]string{}
	applyBdSchemaSkewOverrideFromWorkspace(env, workspace, func(string) (string, bool) { return "", false })
	if got, ok := env["BD_IGNORE_SCHEMA_SKEW"]; ok {
		t.Fatalf("BD_IGNORE_SCHEMA_SKEW = %q, want absent for an unconfigured city", got)
	}
}

// TestCityRuntimeProcessEnvCarriesWorkspaceSchemaSkewOverride is the reported
// ga-ili failure. gc session reset runs its bead read inside the controller,
// whose environment was captured at city start, so an operator's shell export
// can never reach it. Only a city-declared posture can.
func TestCityRuntimeProcessEnvCarriesWorkspaceSchemaSkewOverride(t *testing.T) {
	cityPath := t.TempDir()
	writeSkewCityTOML(t, cityPath, `BD_IGNORE_SCHEMA_SKEW = "1"`)
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")

	env := envEntriesMap(mustCityRuntimeProcessEnv(t, cityPath))
	if got := env["BD_IGNORE_SCHEMA_SKEW"]; got != "1" {
		t.Fatalf("BD_IGNORE_SCHEMA_SKEW = %q, want workspace-configured %q", got, "1")
	}
}

// TestSessionBackendEnvCarriesWorkspaceSchemaSkewOverride covers the agent
// sessions. Their bd calls run inside tmux with an explicitly projected
// environment, so an unprojected key leaves every agent prefixing by hand.
func TestSessionBackendEnvCarriesWorkspaceSchemaSkewOverride(t *testing.T) {
	cityPath := t.TempDir()
	writeSkewCityTOML(t, cityPath, `BD_IGNORE_SCHEMA_SKEW = "1"`)
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")

	env := mustSessionBackendEnv(t, cityPath, "", nil)
	if got := env["BD_IGNORE_SCHEMA_SKEW"]; got != "1" {
		t.Fatalf("BD_IGNORE_SCHEMA_SKEW = %q, want workspace-configured %q", got, "1")
	}
}

// TestSessionBackendEnvUsesSuppliedWorkspaceSchemaSkewOverride covers the
// route production actually takes. The desired-state build already holds the
// loaded [workspace], and passing it keeps the per-agent session projection off
// the config loader entirely, so the supplied section — not a re-read of
// city.toml — must be what decides the key.
func TestSessionBackendEnvUsesSuppliedWorkspaceSchemaSkewOverride(t *testing.T) {
	cityPath := t.TempDir()
	writeSkewCityTOML(t, cityPath, `BD_IGNORE_SCHEMA_SKEW = "0"`)
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")

	workspace := &config.Workspace{Env: map[string]string{"BD_IGNORE_SCHEMA_SKEW": "1"}}
	env, err := sessionBackendEnvWithError(cityPath, "", nil, workspace)
	if err != nil {
		t.Fatalf("sessionBackendEnvWithError() error = %v", err)
	}
	if got := env["BD_IGNORE_SCHEMA_SKEW"]; got != "1" {
		t.Fatalf("BD_IGNORE_SCHEMA_SKEW = %q, want supplied-workspace %q", got, "1")
	}
}

// TestRecoverManagedBDCommandCarriesWorkspaceSchemaSkewOverride covers the
// provider lifecycle script, which shells out to bd with a fully projected
// environment of its own.
func TestRecoverManagedBDCommandCarriesWorkspaceSchemaSkewOverride(t *testing.T) {
	cityPath := t.TempDir()
	writeSkewCityTOML(t, cityPath, `BD_IGNORE_SCHEMA_SKEW = "1"`)
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")

	capture := filepath.Join(t.TempDir(), "recover-env.txt")
	script := gcBeadsBdScriptPath(cityPath)
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"${BD_IGNORE_SCHEMA_SKEW:-}\" > %q\n", capture)
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := recoverManagedBDCommand(cityPath); err != nil {
		t.Fatalf("recoverManagedBDCommand: %v", err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read captured env: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "1" {
		t.Fatalf("BD_IGNORE_SCHEMA_SKEW = %q, want workspace-configured %q", got, "1")
	}
}
