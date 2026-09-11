package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeStateFilePathsListsHomeThenConfigDir(t *testing.T) {
	got := ClaudeStateFilePaths("/home/u", "/home/u/.claude")
	want := []string{"/home/u/.claude.json", "/home/u/.claude/.claude.json"}
	if len(got) != len(want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths = %#v, want %#v", got, want)
		}
	}
}

func TestClaudeStateFilePathsDedupesAndToleratesEmptyInputs(t *testing.T) {
	if got := ClaudeStateFilePaths("/home/u", "/home/u"); len(got) != 1 || got[0] != "/home/u/.claude.json" {
		t.Fatalf("overlapping config dir = %#v, want one path", got)
	}
	if got := ClaudeStateFilePaths("", "/home/u/.claude"); len(got) != 1 || got[0] != "/home/u/.claude/.claude.json" {
		t.Fatalf("empty home = %#v, want config-dir path only", got)
	}
	if got := ClaudeStateFilePaths("  ", "  "); len(got) != 0 {
		t.Fatalf("blank inputs = %#v, want none", got)
	}
}

func TestClaudeTrustedProjectRootsCollectsOnlyAcceptedProjects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	writeClaudeStateFixture(t, path, `{
	  "projects": {
	    "/data/projects/trusted": {"hasTrustDialogAccepted": true},
	    "/data/projects/declined": {"hasTrustDialogAccepted": false},
	    "/data/projects/unseen": {"hasCompletedProjectOnboarding": true},
	    "/data/projects/untidy/": {"hasTrustDialogAccepted": true},
	    "/data/projects/wrongtype": "not-an-object"
	  }
	}`)

	trusted, err := ClaudeTrustedProjectRoots([]string{path})
	if err != nil {
		t.Fatalf("ClaudeTrustedProjectRoots: %v", err)
	}
	if !trusted["/data/projects/trusted"] {
		t.Errorf("accepted project missing from %#v", trusted)
	}
	if !trusted["/data/projects/untidy"] {
		t.Errorf("trailing-slash key not cleaned in %#v", trusted)
	}
	for _, absent := range []string{"/data/projects/declined", "/data/projects/unseen", "/data/projects/wrongtype"} {
		if trusted[absent] {
			t.Errorf("%s should not be trusted in %#v", absent, trusted)
		}
	}
}

func TestClaudeTrustedProjectRootsUnionsEveryStateFile(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "home.json")
	second := filepath.Join(dir, "configdir.json")
	writeClaudeStateFixture(t, first, `{"projects": {"/a": {"hasTrustDialogAccepted": true}}}`)
	writeClaudeStateFixture(t, second, `{"projects": {"/b": {"hasTrustDialogAccepted": true}}}`)

	trusted, err := ClaudeTrustedProjectRoots([]string{first, second})
	if err != nil {
		t.Fatalf("ClaudeTrustedProjectRoots: %v", err)
	}
	if !trusted["/a"] || !trusted["/b"] {
		t.Fatalf("trusted = %#v, want both roots", trusted)
	}
}

func TestClaudeTrustedProjectRootsIgnoresMissingFiles(t *testing.T) {
	trusted, err := ClaudeTrustedProjectRoots([]string{filepath.Join(t.TempDir(), "absent.json")})
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(trusted) != 0 {
		t.Fatalf("trusted = %#v, want empty", trusted)
	}
}

func TestClaudeTrustedProjectRootsReportsMalformedFileWithoutDiscardingGoodOnes(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	bad := filepath.Join(dir, "bad.json")
	writeClaudeStateFixture(t, good, `{"projects": {"/a": {"hasTrustDialogAccepted": true}}}`)
	writeClaudeStateFixture(t, bad, `{not json`)

	trusted, err := ClaudeTrustedProjectRoots([]string{good, bad})
	if err == nil {
		t.Fatal("malformed state file should surface an error")
	}
	if !trusted["/a"] {
		t.Fatalf("trusted = %#v, want the readable file's entry preserved", trusted)
	}
}

func writeClaudeStateFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
