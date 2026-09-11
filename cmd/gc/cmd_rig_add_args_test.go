package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ga-lyl: `gc rig add <name> <path>` reads naturally but is not the signature.
// The one positional argument is the path; the name rides --name. Cobra accepts
// arbitrary args here (the remote path takes none), so the local add used to
// take args[0] as the path and drop the rest without a word. A bare args[0]
// resolves relative to the city, so `gc rig add hivemind /data/projects/hivemind`
// silently provisioned $GC_CITY/hivemind — a stray directory registered as the
// rig root, leaving the rig unable to do any git work.

func TestRigAddLocalArgsRefusalAcceptsZeroOrOneArg(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"/data/projects/hivemind"}} {
		if msg, refused := rigAddLocalArgsRefusal(args); refused {
			t.Errorf("args %v refused with %q; one positional path is the documented form", args, msg)
		}
	}
}

func TestRigAddLocalArgsRefusalRejectsExtraPositionals(t *testing.T) {
	args := []string{"hivemind", "/data/projects/hivemind"}
	msg, refused := rigAddLocalArgsRefusal(args)
	if !refused {
		t.Fatalf("args %v accepted; the second positional is silently dropped and the add provisions $GC_CITY/hivemind", args)
	}
	// The message must name the real cause and the working form, because the
	// failure is otherwise invisible: the add "succeeds" against the wrong path.
	for _, want := range []string{"gc rig add:", "one positional argument", "--name", "hivemind", "/data/projects/hivemind"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not mention %q", msg, want)
		}
	}
}

func TestRigAddLocalArgsRefusalReportsEveryExtraArg(t *testing.T) {
	msg, refused := rigAddLocalArgsRefusal([]string{"a", "b", "c"})
	if !refused {
		t.Fatal("three positionals accepted")
	}
	for _, want := range []string{"a", "b", "c"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q omits argument %q", msg, want)
		}
	}
}

// The refusal is the local path's guard only: a remote add already refuses any
// positional argument with its own text, and that text is gated as
// byte-identical. Keep the two from drifting into each other.
func TestRigAddRemoteStillRefusesPositionalArgsItself(t *testing.T) {
	code, msg, refused := rigAddRemoteRefusal([]string{"hivemind", "/data/projects/hivemind"}, "", false, nil, false)
	if !refused {
		t.Fatal("remote add accepted positional args")
	}
	if code != "unsupported_remote" {
		t.Errorf("remote refusal code = %q, want unsupported_remote", code)
	}
	if !strings.Contains(msg, "--git-url") {
		t.Errorf("remote refusal %q no longer points at --git-url", msg)
	}
}

// newRigAddArgsCity writes the smallest city `gc rig add` will run against.
func newRigAddArgsCity(t *testing.T) string {
	t.Helper()
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_DOLT", "skip")
	t.Setenv("GC_SESSION", "fake")

	cityPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityPath, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"),
		[]byte("[workspace]\nname = \"test\"\n\n[[agent]]\nname = \"mayor\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return cityPath
}

// The end of the reported failure: the extra positional is dropped, args[0] is
// a bare name, a bare name resolves against the city, and the add provisions
// $GC_CITY/<name> — a directory that is not a checkout and cannot do git work.
func TestRigAddRefusesNameThenPathAndProvisionsNothing(t *testing.T) {
	cityPath := newRigAddArgsCity(t)
	rigPath := filepath.Join(t.TempDir(), "hivemind")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--city", cityPath, "rig", "add", "hivemind", rigPath}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("gc rig add %s %s succeeded; stdout: %s", "hivemind", rigPath, stdout.String())
	}
	if !strings.Contains(stderr.String(), "one positional argument") {
		t.Errorf("stderr %q does not explain the argument shape", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(cityPath, "hivemind")); !os.IsNotExist(err) {
		t.Errorf("%s was provisioned from the dropped positional (stat err = %v)", filepath.Join(cityPath, "hivemind"), err)
	}
	if got := readCityTOML(t, cityPath); strings.Contains(got, "[[rig]]") {
		t.Errorf("a rig was registered by a refused add:\n%s", got)
	}
}

// --json callers must see the refusal as a structured error, not a bare exit
// code, and not as a success object describing a rig that was never added.
func TestRigAddRefusesExtraPositionalsInJSONMode(t *testing.T) {
	cityPath := newRigAddArgsCity(t)
	rigPath := filepath.Join(t.TempDir(), "hivemind")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--city", cityPath, "rig", "add", "hivemind", rigPath, "--json"}, &stdout, &stderr); code == 0 {
		t.Fatalf("gc rig add --json succeeded; stdout: %s", stdout.String())
	}
	var got struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not the JSON error envelope (%v): %s", err, stdout.String())
	}
	if got.OK {
		t.Error("refused add reported ok=true")
	}
	if got.Error.Code != "invalid_arguments" {
		t.Errorf("error code = %q, want invalid_arguments", got.Error.Code)
	}
	if !strings.Contains(got.Error.Message, "one positional argument") {
		t.Errorf("error message %q does not explain the argument shape", got.Error.Message)
	}
}

// readCityTOML reads the city config a refused add must have left untouched.
func readCityTOML(t *testing.T, cityPath string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cityPath, "city.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
