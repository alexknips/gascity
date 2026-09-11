package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/fsys"
	beadsschema "github.com/steveyegge/beads/schema"
)

// allowSchemaMigrationEnv disarms the native-open schema-migration gate for a
// caller that deliberately intends to migrate this city's databases. It is the
// designated-migrator escape hatch — the local-server twin of beads' own
// BD_ALLOW_REMOTE_MIGRATE, which cannot help here because gc's managed
// databases have no Dolt remote for that gate to notice. Set it only while
// performing an intended, supervised migration of the whole host.
const allowSchemaMigrationEnv = "GC_ALLOW_NATIVE_SCHEMA_MIGRATION"

func allowNativeSchemaMigration() bool {
	allowed, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(allowSchemaMigrationEnv)))
	return err == nil && allowed
}

func newBeadsPreflightChecker(cityPath, provider string) contract.PreflightChecker {
	return contract.PreflightChecker{
		FS:                        fsys.OSFS{},
		Provider:                  provider,
		BDContext:                 preflightBDContextReader(cityPath),
		DatabaseProjectID:         preflightDatabaseProjectIDReader(cityPath),
		DeferIdentityToNativeOpen: preflightIdentityDeferredReader(cityPath),
		DatabaseSchemaVersion:     preflightDatabaseSchemaVersionReader(cityPath),
		// The version a native open would leave the database at: beadslib
		// applies every migration up to LatestVersion() before serving a
		// query. Reading it from the linked package rather than a constant
		// means bumping the beads pin cannot silently outrun this gate.
		LinkedSchemaVersion:  beadsschema.LatestVersion(),
		AllowSchemaMigration: allowNativeSchemaMigration(),
	}
}

func preflightBDContextReader(cityPath string) func(scope string) (contract.PreflightBDContext, error) {
	return func(scope string) (contract.PreflightBDContext, error) {
		out, err := bdCommandRunnerForCity(cityPath)(scope, "bd", "context", "--json")
		if err != nil {
			return contract.PreflightBDContext{}, err
		}
		var raw struct {
			Backend       string `json:"backend"`
			DoltMode      string `json:"dolt_mode"`
			BDVersion     string `json:"bd_version"`
			SchemaVersion int    `json:"schema_version"`
		}
		if err := json.Unmarshal(out, &raw); err != nil {
			return contract.PreflightBDContext{}, fmt.Errorf("parse bd context --json: %w", err)
		}
		return contract.PreflightBDContext{
			Backend:       raw.Backend,
			DoltMode:      raw.DoltMode,
			BDVersion:     raw.BDVersion,
			SchemaVersion: raw.SchemaVersion,
		}, nil
	}
}

// preflightIdentityDeferredReader reports whether a scope resolves to an
// external Dolt endpoint (e.g. a hosted beads-gateway). The direct root/plaintext
// project_id probe cannot authenticate such endpoints, so when it comes back
// unconfirmed the identity check defers to beadslib's native-open verification
// (which authenticates via the credential command and refuses to connect on a
// _project_id mismatch) instead of degrading the scope off the native store.
func preflightIdentityDeferredReader(cityPath string) func(scope string) bool {
	return func(scope string) bool {
		target, ok, err := canonicalScopeDoltTarget(cityPath, scope)
		if err != nil || !ok {
			return false
		}
		return target.External
	}
}

// preflightDatabaseSchemaVersionReader reports the beads schema migration
// version already applied to a scope's database. It is the "before" side of the
// native-open schema gate: compared against the version the linked beads
// library would migrate to, it says whether opening the native store would move
// this city's schema. Mirrors preflightDatabaseProjectIDReader's probe — a
// database that has never been migrated reports 0, not an error.
func preflightDatabaseSchemaVersionReader(cityPath string) func(scope string) (int, bool, error) {
	return func(scope string) (int, bool, error) {
		target, ok, err := canonicalScopeDoltTarget(cityPath, scope)
		if err != nil || !ok {
			return 0, false, err
		}
		// Pooled handle owned by internal/doltpool; do not Close.
		db, err := managedDoltOpenDatabase(target.Host, target.Port, target.User, target.Database)
		if err != nil {
			return 0, false, err
		}

		// No separate liveness ping: the sibling project_id probe already
		// pays that cost on this same pooled handle, and the query below is
		// bounded by the same context. Pinging again only doubles how long an
		// unreachable Dolt server stalls the preflight.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		version, err := beadsschema.CurrentVersion(ctx, db)
		if err != nil {
			return 0, false, err
		}
		return version, true, nil
	}
}

func preflightDatabaseProjectIDReader(cityPath string) func(scope string) (string, bool, error) {
	return func(scope string) (string, bool, error) {
		target, ok, err := canonicalScopeDoltTarget(cityPath, scope)
		if err != nil || !ok {
			return "", false, err
		}
		// Pooled handle owned by internal/doltpool; do not Close.
		db, err := managedDoltOpenDatabase(target.Host, target.Port, target.User, target.Database)
		if err != nil {
			return "", false, err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			return "", false, err
		}
		return readDatabaseProjectID(ctx, db)
	}
}
