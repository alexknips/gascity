package contract

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/fsys"
)

// schemaGateScope is the scope every case below probes.
const schemaGateScope = "/city"

// newSchemaGateChecker builds a checker whose every other check passes, so the
// verdict below is attributable to the schema-migration gate alone.
func newSchemaGateChecker(t *testing.T) PreflightChecker {
	t.Helper()
	fs := fsys.NewFake()
	fs.Dirs[filepath.Join(schemaGateScope, ".beads")] = true
	fs.Files[filepath.Join(schemaGateScope, ".beads", "metadata.json")] = []byte(`{
		"backend": "dolt",
		"dolt_mode": "server",
		"dolt_database": "gascity",
		"project_id": "gc-local"
	}`)
	return PreflightChecker{
		FS:                  fs,
		Provider:            "bd",
		BeadsLibraryVersion: "1.0.4",
		BDContext: func(string) (PreflightBDContext, error) {
			return PreflightBDContext{Backend: "dolt", DoltMode: "server", BDVersion: "1.0.4", SchemaVersion: 1}, nil
		},
		DatabaseProjectID: func(string) (string, bool, error) {
			return "gc-local", true, nil
		},
	}
}

func schemaVersionReader(version int, ok bool, err error) func(string) (int, bool, error) {
	return func(string) (int, bool, error) { return version, ok, err }
}

// TestPreflightBlocksNativeWhenOpenWouldMigrateSchema is the ga-o6k
// regression: a gc whose linked beads library carries migrations the database
// has not applied must not be allowed to open the native store, because the
// open applies them in place and strands every installed bd binary on this
// host. The scope stays usable through BdStore.
func TestPreflightBlocksNativeWhenOpenWouldMigrateSchema(t *testing.T) {
	checker := newSchemaGateChecker(t)
	checker.DatabaseSchemaVersion = schemaVersionReader(53, true, nil)
	checker.LinkedSchemaVersion = 59

	result, err := checker.Check(schemaGateScope)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	assertPreflightVerdict(t, result, PreflightVerdictBlocked, false)
	assertCheckState(t, result, PreflightCheckSchemaMigration, PreflightCheckFail)
	if result.Fallback != PreflightFallbackBdStore {
		t.Errorf("Fallback = %q, want %q", result.Fallback, PreflightFallbackBdStore)
	}
	summary := checkSummary(t, result, PreflightCheckSchemaMigration)
	for _, want := range []string{"v53", "v59"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q does not name %s", summary, want)
		}
	}
}

// TestPreflightBlocksNativeWhenSchemaIsAheadOfLinkedLibrary covers the
// after-the-fact half of the same skew: once some other binary has migrated
// the store past this one, this binary cannot serve it and must say so rather
// than fail opaquely inside the library's open.
func TestPreflightBlocksNativeWhenSchemaIsAheadOfLinkedLibrary(t *testing.T) {
	checker := newSchemaGateChecker(t)
	checker.DatabaseSchemaVersion = schemaVersionReader(59, true, nil)
	checker.LinkedSchemaVersion = 53

	result, err := checker.Check(schemaGateScope)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	assertPreflightVerdict(t, result, PreflightVerdictBlocked, false)
	assertCheckState(t, result, PreflightCheckSchemaMigration, PreflightCheckFail)
}

func TestPreflightAllowsNativeWhenSchemaMatchesLinkedLibrary(t *testing.T) {
	checker := newSchemaGateChecker(t)
	checker.DatabaseSchemaVersion = schemaVersionReader(53, true, nil)
	checker.LinkedSchemaVersion = 53

	result, err := checker.Check(schemaGateScope)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	assertPreflightVerdict(t, result, PreflightVerdictEligible, true)
	assertCheckState(t, result, PreflightCheckSchemaMigration, PreflightCheckPass)
}

// TestPreflightAllowsNativeOnUnmigratedDatabase keeps bootstrap working: a
// database with no schema_migrations cursor has no city ledger to strand, so
// the first open is allowed to create the schema.
func TestPreflightAllowsNativeOnUnmigratedDatabase(t *testing.T) {
	checker := newSchemaGateChecker(t)
	checker.DatabaseSchemaVersion = schemaVersionReader(0, true, nil)
	checker.LinkedSchemaVersion = 59

	result, err := checker.Check(schemaGateScope)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	assertPreflightVerdict(t, result, PreflightVerdictEligible, true)
	assertCheckState(t, result, PreflightCheckSchemaMigration, PreflightCheckPass)
}

// TestPreflightSchemaGateHonorsDesignatedMigrator lets an operator who is
// deliberately migrating the host through with the gate disarmed.
func TestPreflightSchemaGateHonorsDesignatedMigrator(t *testing.T) {
	checker := newSchemaGateChecker(t)
	checker.DatabaseSchemaVersion = schemaVersionReader(53, true, nil)
	checker.LinkedSchemaVersion = 59
	checker.AllowSchemaMigration = true

	result, err := checker.Check(schemaGateScope)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	assertPreflightVerdict(t, result, PreflightVerdictEligible, true)
	assertCheckState(t, result, PreflightCheckSchemaMigration, PreflightCheckPass)
}

// TestPreflightSchemaGateFailsSafeWithoutEvidence pins the fail-closed
// direction: with no probe, no readable version, or no linked-library version,
// nothing proves a native open would leave the schema alone, so the scope
// degrades to BdStore instead of being assumed safe.
func TestPreflightSchemaGateFailsSafeWithoutEvidence(t *testing.T) {
	cases := []struct {
		name   string
		reader func(string) (int, bool, error)
		linked int
	}{
		{name: "no reader configured", reader: nil, linked: 53},
		{name: "probe errored", reader: schemaVersionReader(0, false, errors.New("dial tcp: connection refused")), linked: 53},
		{name: "probe unconfirmed", reader: schemaVersionReader(0, false, nil), linked: 53},
		{name: "linked version unknown", reader: schemaVersionReader(53, true, nil), linked: 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			checker := newSchemaGateChecker(t)
			checker.DatabaseSchemaVersion = tt.reader
			checker.LinkedSchemaVersion = tt.linked

			result, err := checker.Check(schemaGateScope)
			if err != nil {
				t.Fatalf("Check() error = %v", err)
			}

			if result.NativeStoreEligible {
				t.Fatalf("NativeStoreEligible = true, want false; checks=%+v", result.Checks)
			}
			assertCheckState(t, result, PreflightCheckSchemaMigration, PreflightCheckWarn)
		})
	}
}

// TestPreflightSchemaGateDefersForExternalEndpoint keeps a hosted endpoint as
// eligible as it is today: the root/plaintext probe cannot authenticate one,
// and such a database carries a Dolt remote, so beads' own remote-migrate gate
// covers it.
func TestPreflightSchemaGateDefersForExternalEndpoint(t *testing.T) {
	checker := newSchemaGateChecker(t)
	checker.DatabaseSchemaVersion = schemaVersionReader(0, false, errors.New("unauthenticated"))
	checker.LinkedSchemaVersion = 53
	checker.DeferIdentityToNativeOpen = func(string) bool { return true }

	result, err := checker.Check(schemaGateScope)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	assertPreflightVerdict(t, result, PreflightVerdictEligible, true)
	assertCheckState(t, result, PreflightCheckSchemaMigration, PreflightCheckPass)
}

// TestPreflightSchemaGateOffersDesignatedMigratorRepair asserts the blocked
// scope tells an operator how to resolve it deliberately.
func TestPreflightSchemaGateOffersDesignatedMigratorRepair(t *testing.T) {
	checker := newSchemaGateChecker(t)
	checker.DatabaseSchemaVersion = schemaVersionReader(53, true, nil)
	checker.LinkedSchemaVersion = 59

	result, err := checker.Check(schemaGateScope)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	for _, step := range result.RepairSteps {
		if step.CheckID == PreflightCheckSchemaMigration {
			if step.Priority != PreflightRepairCritical {
				t.Errorf("repair priority = %q, want %q", step.Priority, PreflightRepairCritical)
			}
			return
		}
	}
	t.Fatalf("no repair step for %q; steps=%+v", PreflightCheckSchemaMigration, result.RepairSteps)
}

func checkSummary(t *testing.T, result PreflightResult, id PreflightCheckID) string {
	t.Helper()
	for _, check := range result.Checks {
		if check.ID == id {
			return check.Summary
		}
	}
	t.Fatalf("check %q not found", id)
	return ""
}
