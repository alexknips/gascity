package tmux

import (
	"errors"
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

// TestBinaryIsStable pins the caching contract. The whole point of resolving
// once is that a later PATH change cannot make a subsequent gc call reach a
// different tmux than the one already driving the server.
func TestBinaryIsStable(t *testing.T) {
	first := Binary()
	t.Setenv("PATH", "/nonexistent")
	if second := Binary(); second != first {
		t.Fatalf("Binary() changed after PATH change: %q then %q", first, second)
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
