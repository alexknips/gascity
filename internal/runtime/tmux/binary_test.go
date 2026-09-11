package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBinaryPrefersPinnedEnv(t *testing.T) {
	getenv := func(key string) string {
		if key == BinaryEnv {
			return "  /opt/pinned/tmux  "
		}
		return ""
	}
	lookPath := func(string) (string, error) {
		t.Fatal("PATH must not be consulted when the binary is pinned")
		return "", nil
	}
	if got, want := resolveBinary(getenv, lookPath), "/opt/pinned/tmux"; got != want {
		t.Fatalf("resolveBinary() = %q, want %q", got, want)
	}
}

func TestResolveBinaryUsesPathWhenUnpinned(t *testing.T) {
	getenv := func(string) string { return "" }
	lookPath := func(name string) (string, error) {
		if name != DefaultBinary {
			t.Fatalf("lookPath(%q), want %q", name, DefaultBinary)
		}
		return "/home/linuxbrew/.linuxbrew/bin/tmux", nil
	}
	if got, want := resolveBinary(getenv, lookPath), "/home/linuxbrew/.linuxbrew/bin/tmux"; got != want {
		t.Fatalf("resolveBinary() = %q, want %q", got, want)
	}
}

// TestResolveBinaryFallsBackToBareName pins the degraded case: with nothing on
// PATH there is no better argument to hand exec than the plain name, and the
// resulting error should name tmux the way callers expect.
func TestResolveBinaryFallsBackToBareName(t *testing.T) {
	getenv := func(string) string { return "" }
	lookPath := func(string) (string, error) { return "", errors.New("not found") }
	if got, want := resolveBinary(getenv, lookPath), DefaultBinary; got != want {
		t.Fatalf("resolveBinary() = %q, want %q", got, want)
	}
}

// TestResolveBinaryIgnoresEmptyLookPathResult guards against a LookPath
// implementation that returns no error and no path: resolving to "" would hand
// exec an empty command name.
func TestResolveBinaryIgnoresEmptyLookPathResult(t *testing.T) {
	getenv := func(string) string { return "" }
	lookPath := func(string) (string, error) { return "", nil }
	if got, want := resolveBinary(getenv, lookPath), DefaultBinary; got != want {
		t.Fatalf("resolveBinary() = %q, want %q", got, want)
	}
}

// TestBinaryHonoursThePinFromTheCurrentEnvironment pins the guarantee an
// operator actually buys with GC_TMUX_BIN: the pinned path is what gc execs,
// with no PATH involved and no earlier resolution remembered in its place.
func TestBinaryHonoursThePinFromTheCurrentEnvironment(t *testing.T) {
	t.Setenv(BinaryEnv, "/opt/first/tmux")
	if got, want := Binary(), "/opt/first/tmux"; got != want {
		t.Fatalf("Binary() = %q, want %q", got, want)
	}
	t.Setenv(BinaryEnv, "/opt/second/tmux")
	if got, want := Binary(), "/opt/second/tmux"; got != want {
		t.Fatalf("Binary() after repin = %q, want %q", got, want)
	}
}

// TestBinaryDoesNotKeepReturningARemovedPath is the regression test for the
// defect that made resolving once per process untenable. A resolved absolute
// path can stop naming a file — a package upgrade unlinks the keg under a
// long-lived process, a test's temporary directory is cleaned up — and a
// remembered path would make every later call exec something that is gone.
// The observed symptom was "fork/exec <removed dir>/tmux: no such file or
// directory" in processes that had resolved tmux long before.
func TestBinaryDoesNotKeepReturningARemovedPath(t *testing.T) {
	t.Setenv(BinaryEnv, "")
	dir := t.TempDir()
	stub := filepath.Join(dir, DefaultBinary)
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write tmux stub: %v", err)
	}
	t.Setenv("PATH", dir)

	if got := Binary(); got != stub {
		t.Fatalf("Binary() = %q, want the stub at %q", got, stub)
	}

	if err := os.Remove(stub); err != nil {
		t.Fatalf("remove tmux stub: %v", err)
	}
	if got := Binary(); got == stub {
		t.Fatalf("Binary() = %q, want anything but the removed path", got)
	}
}

// TestBinaryFollowsPathToALiveTmux is the other half of the same contract:
// having dropped a path that disappeared, the next call must find whatever
// tmux the current environment does name, not give up on the bare name.
func TestBinaryFollowsPathToALiveTmux(t *testing.T) {
	t.Setenv(BinaryEnv, "")
	stale, live := t.TempDir(), t.TempDir()
	staleStub := filepath.Join(stale, DefaultBinary)
	liveStub := filepath.Join(live, DefaultBinary)
	for _, stub := range []string{staleStub, liveStub} {
		if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write tmux stub %s: %v", stub, err)
		}
	}

	t.Setenv("PATH", stale)
	if got := Binary(); got != staleStub {
		t.Fatalf("Binary() = %q, want %q", got, staleStub)
	}

	if err := os.Remove(staleStub); err != nil {
		t.Fatalf("remove tmux stub: %v", err)
	}
	t.Setenv("PATH", live)
	if got := Binary(); got != liveStub {
		t.Fatalf("Binary() = %q, want the surviving tmux at %q", got, liveStub)
	}
}

func TestParseVersionOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{"linuxbrew", "tmux 3.7c\n", "3.7c"},
		{"distro", "tmux 3.4\n", "3.4"},
		{"next release candidate", "tmux next-3.8\n", "next-3.8"},
		{"no trailing newline", "tmux 3.5a", "3.5a"},
		{"empty", "", ""},
		{"name only", "tmux\n", ""},
		{"not tmux", "sh: tmux: command not found\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseVersionOutput(tc.out); got != tc.want {
				t.Fatalf("parseVersionOutput(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}
