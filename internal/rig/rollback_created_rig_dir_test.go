package rig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// ga-lyl: when `gc rig add` is handed a path that does not exist yet, it
// creates the directory (step 10) and then keeps provisioning. If a later step
// fails, that directory stays behind — and because a bare relative argument
// resolves against the city, the debris lands at $GC_CITY/<name>. The operator
// then reruns with --adopt, which finds the leftover, treats it as a real
// checkout, and registers it as the rig root. The rig can do no git work and
// recovery means hand-editing city.toml.
//
// A directory this call created and left empty is unambiguously ours to remove.
// A directory that already existed, or one holding anything at all, is not.

// provisionIntoMissingDir wires a fresh add whose rig path does not exist yet,
// so Provision is the thing that creates it.
func provisionIntoMissingDir(t *testing.T, initStore func(cityPath, dir, prefix string) (bool, error)) (Deps, ProvisionRequest) {
	t.Helper()
	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(originalCityTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := stubDeps(cityPath)
	deps.NormalizeScopes = func(string, *config.City) error { return nil }
	deps.InitStore = initStore
	// Mirrors the reported shape: a bare name resolved against the city root.
	return deps, ProvisionRequest{Name: "strayrig", Path: filepath.Join(cityPath, "strayrig")}
}

func TestProvisionRemovesRigDirItCreatedWhenProvisioningFails(t *testing.T) {
	initErr := errors.New("managed Dolt server unreachable")
	deps, req := provisionIntoMissingDir(t, func(_, _, _ string) (bool, error) {
		return false, initErr
	})

	if _, err := os.Stat(req.Path); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s must not exist before the add (stat err = %v)", req.Path, err)
	}

	if _, _, provErr := Provision(deps, req); provErr == nil {
		t.Fatal("expected the store-init failure to surface")
	}

	if _, err := os.Stat(req.Path); !os.IsNotExist(err) {
		t.Fatalf("%s survived a failed rig add (stat err = %v); a later --adopt will register this stray path as the rig root", req.Path, err)
	}
}

// The partial bead store is removed first, which empties the directory — the
// created directory must then go too, not linger because init got that far.
func TestProvisionRemovesCreatedRigDirAfterPartialStoreCleanup(t *testing.T) {
	initErr := errors.New("boom")
	deps, req := provisionIntoMissingDir(t, func(_, dir, _ string) (bool, error) {
		writePartialStore(t, dir)
		return false, initErr
	})

	if _, _, provErr := Provision(deps, req); provErr == nil {
		t.Fatal("expected the store-init failure to surface")
	}

	if _, err := os.Stat(req.Path); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(req.Path)
		t.Fatalf("%s survived a failed rig add (entries = %v)", req.Path, entries)
	}
}

// A directory that was already on disk is the user's, even when empty and even
// when the add fails. Provision did not create it and must not remove it.
func TestProvisionKeepsPreExistingRigDirWhenProvisioningFails(t *testing.T) {
	initErr := errors.New("boom")
	deps, req := provisionIntoMissingDir(t, func(_, _, _ string) (bool, error) {
		return false, initErr
	})
	if err := os.MkdirAll(req.Path, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, provErr := Provision(deps, req); provErr == nil {
		t.Fatal("expected the store-init failure to surface")
	}

	if _, err := os.Stat(req.Path); err != nil {
		t.Fatalf("pre-existing rig directory was removed by the rollback: %v", err)
	}
}

// Fail safe: anything left in the directory means we cannot prove it is only
// our debris, so it stays. Deleting user content is far worse than leaving a
// stray directory behind.
func TestProvisionKeepsCreatedRigDirHoldingContentWhenProvisioningFails(t *testing.T) {
	initErr := errors.New("boom")
	deps, req := provisionIntoMissingDir(t, func(_, dir, _ string) (bool, error) {
		if err := os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte("user content"), 0o644); err != nil {
			return false, err
		}
		return false, initErr
	})

	if _, _, provErr := Provision(deps, req); provErr == nil {
		t.Fatal("expected the store-init failure to surface")
	}

	if _, err := os.Stat(filepath.Join(req.Path, "NOTES.md")); err != nil {
		t.Fatalf("content in the created rig directory was destroyed by the rollback: %v", err)
	}
}

// A successful add must keep the directory it created.
func TestProvisionKeepsCreatedRigDirOnSuccess(t *testing.T) {
	deps, req := provisionIntoMissingDir(t, func(_, _, _ string) (bool, error) { return false, nil })

	if _, _, provErr := Provision(deps, req); provErr != nil {
		t.Fatalf("provision failed: %v", provErr)
	}

	if _, err := os.Stat(req.Path); err != nil {
		t.Fatalf("rig directory missing after a successful add: %v", err)
	}
}
