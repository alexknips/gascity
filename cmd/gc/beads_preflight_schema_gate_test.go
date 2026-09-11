package main

import (
	"testing"

	beadsschema "github.com/steveyegge/beads/schema"
)

// TestBeadsPreflightCheckerWiresSchemaMigrationGate closes the fail-open hole
// in the ga-o6k gate. checkSchemaMigration can only refuse a migrating native
// open when the composition root hands it both a database probe and the version
// the linked beads library would migrate to; an unwired checker degrades every
// scope to BdStore instead, which is safe but silently costs the native store.
// This is the single production construction site, so asserting it here is what
// keeps the gate armed.
func TestBeadsPreflightCheckerWiresSchemaMigrationGate(t *testing.T) {
	checker := newBeadsPreflightChecker(t.TempDir(), "bd")

	if checker.DatabaseSchemaVersion == nil {
		t.Error("DatabaseSchemaVersion is not wired; the schema-migration gate has no evidence to act on")
	}
	if checker.LinkedSchemaVersion <= 0 {
		t.Errorf("LinkedSchemaVersion = %d, want the linked beads library's LatestVersion()", checker.LinkedSchemaVersion)
	}
	if want := beadsschema.LatestVersion(); checker.LinkedSchemaVersion != want {
		t.Errorf("LinkedSchemaVersion = %d, want %d (the version a native open would leave behind)", checker.LinkedSchemaVersion, want)
	}
}

// TestAllowNativeSchemaMigrationRequiresExplicitOptIn pins the escape hatch to
// boolean opt-in only: a typo'd or empty value must leave the gate armed.
func TestAllowNativeSchemaMigrationRequiresExplicitOptIn(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"yes", false},
		{"please", false},
		{"1", true},
		{"true", true},
		{" true ", true},
	}
	for _, tt := range tests {
		t.Run("value="+tt.value, func(t *testing.T) {
			t.Setenv(allowSchemaMigrationEnv, tt.value)
			if got := allowNativeSchemaMigration(); got != tt.want {
				t.Errorf("allowNativeSchemaMigration() with %s=%q = %v, want %v", allowSchemaMigrationEnv, tt.value, got, tt.want)
			}
		})
	}
}
