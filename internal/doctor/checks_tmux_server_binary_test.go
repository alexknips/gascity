package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sessiontmux "github.com/gastownhall/gascity/internal/runtime/tmux"
)

// newTestTmuxServerBinaryCheck builds a check whose every external observation
// is stubbed, so the failure conditions can be exercised without a tmux server.
func newTestTmuxServerBinaryCheck() *TmuxServerBinaryCheck {
	return &TmuxServerBinaryCheck{
		socketName:     "gc",
		clientBinary:   func() string { return "/home/linuxbrew/.linuxbrew/bin/tmux" },
		clientVersion:  func(context.Context, string) (string, error) { return "3.7c", nil },
		serverVersion:  func(context.Context, string) (string, error) { return "3.7c", nil },
		pathCandidates: func() []string { return nil },
		inspectServer: func(context.Context, string) (*sessiontmux.ServerBinary, error) {
			return &sessiontmux.ServerBinary{
				PID:  25697,
				Path: "/home/linuxbrew/.linuxbrew/Cellar/tmux/3.7c/bin/tmux",
			}, nil
		},
	}
}

func TestTmuxServerBinaryCheckPassesWhenClientAndServerMatch(t *testing.T) {
	got := newTestTmuxServerBinaryCheck().Run(&CheckContext{})
	if got.Status != StatusOK {
		t.Fatalf("Status = %v, want %v (message: %s)", got.Status, StatusOK, got.Message)
	}
	if !strings.Contains(got.Message, "3.7c") {
		t.Fatalf("Message = %q, want it to name the shared version", got.Message)
	}
}

func TestTmuxServerBinaryCheckPassesWhenNoServerIsRunning(t *testing.T) {
	c := newTestTmuxServerBinaryCheck()
	c.inspectServer = func(context.Context, string) (*sessiontmux.ServerBinary, error) {
		return nil, fmt.Errorf("%w: /tmp/tmux-1000/gc", sessiontmux.ErrNoServerSocket)
	}
	c.serverVersion = func(context.Context, string) (string, error) {
		return "", errors.New("no server running on /tmp/tmux-1000/gc")
	}
	got := c.Run(&CheckContext{})
	if got.Status != StatusOK {
		t.Fatalf("Status = %v, want %v (message: %s)", got.Status, StatusOK, got.Message)
	}
}

// TestTmuxServerBinaryCheckWarnsWhenSocketLookupDisagreesWithTheClient guards
// the false pass: if the socket is not where this check looks but the client
// reaches a server anyway, "no server" is a wrong answer, not a healthy one.
func TestTmuxServerBinaryCheckWarnsWhenSocketLookupDisagreesWithTheClient(t *testing.T) {
	c := newTestTmuxServerBinaryCheck()
	c.inspectServer = func(context.Context, string) (*sessiontmux.ServerBinary, error) {
		return nil, fmt.Errorf("%w: /tmp/tmux-1000/gc", sessiontmux.ErrNoServerSocket)
	}
	got := c.Run(&CheckContext{})
	if got.Status != StatusWarning {
		t.Fatalf("Status = %v, want %v (message: %s)", got.Status, StatusWarning, got.Message)
	}
	if !strings.Contains(got.Message, "reached a server") {
		t.Fatalf("Message = %q, want it to say the client reached a server", got.Message)
	}
}

// TestTmuxServerBinaryCheckFailsOnDeletedServerBinary covers the condition the
// PATH-only tmux check cannot see: the server keeps working on an unlinked
// inode after a package upgrade, and can only be moved onto the live build by
// a restart that drops every session.
func TestTmuxServerBinaryCheckFailsOnDeletedServerBinary(t *testing.T) {
	c := newTestTmuxServerBinaryCheck()
	c.inspectServer = func(context.Context, string) (*sessiontmux.ServerBinary, error) {
		return &sessiontmux.ServerBinary{
			PID:     25697,
			Path:    "/home/linuxbrew/.linuxbrew/Cellar/tmux/3.7b/bin/tmux",
			Deleted: true,
		}, nil
	}
	got := c.Run(&CheckContext{})
	if got.Status != StatusError {
		t.Fatalf("Status = %v, want %v (message: %s)", got.Status, StatusError, got.Message)
	}
	if !strings.Contains(got.Message, "deleted") || !strings.Contains(got.Message, "25697") {
		t.Fatalf("Message = %q, want it to name the deleted executable and the pid", got.Message)
	}
	if got.Severity != SeverityAdvisory {
		t.Fatalf("Severity = %v, want %v: the only remediation is an operator-scheduled restart",
			got.Severity, SeverityAdvisory)
	}
}

// TestTmuxServerBinaryCheckFailsOnVersionMismatch is the regression this check
// was written for. The live host that motivated it ran a 3.7c client against a
// 3.7b server; a 3.4 client against the same server fails outright.
func TestTmuxServerBinaryCheckFailsOnVersionMismatch(t *testing.T) {
	c := newTestTmuxServerBinaryCheck()
	c.clientVersion = func(_ context.Context, binary string) (string, error) {
		if binary == "/home/linuxbrew/.linuxbrew/bin/tmux" {
			return "3.7c", nil
		}
		return "", fmt.Errorf("unexpected binary %q", binary)
	}
	c.serverVersion = func(context.Context, string) (string, error) { return "3.7b", nil }

	got := c.Run(&CheckContext{})
	if got.Status != StatusError {
		t.Fatalf("Status = %v, want %v (message: %s)", got.Status, StatusError, got.Message)
	}
	if !strings.Contains(got.Message, "3.7c") || !strings.Contains(got.Message, "3.7b") {
		t.Fatalf("Message = %q, want both versions named", got.Message)
	}
}

// TestTmuxServerBinaryCheckFailsWhenClientCannotQueryServer covers the shape
// the crash loop actually takes: the socket is live and its peer credentials
// read fine, but the client cannot speak the server's protocol, so it reports
// the server as gone.
func TestTmuxServerBinaryCheckFailsWhenClientCannotQueryServer(t *testing.T) {
	c := newTestTmuxServerBinaryCheck()
	c.serverVersion = func(context.Context, string) (string, error) {
		return "", errors.New("server exited unexpectedly")
	}
	got := c.Run(&CheckContext{})
	if got.Status != StatusError {
		t.Fatalf("Status = %v, want %v (message: %s)", got.Status, StatusError, got.Message)
	}
	if !strings.Contains(got.Message, "server exited unexpectedly") {
		t.Fatalf("Message = %q, want the client's own error quoted", got.Message)
	}
}

// TestTmuxServerBinaryCheckReportsBothProblemsTogether guards against a fix
// for one condition hiding the other: the motivating host had a deleted server
// binary and a version mismatch at the same time.
func TestTmuxServerBinaryCheckReportsBothProblemsTogether(t *testing.T) {
	c := newTestTmuxServerBinaryCheck()
	c.inspectServer = func(context.Context, string) (*sessiontmux.ServerBinary, error) {
		return &sessiontmux.ServerBinary{
			PID:     25697,
			Path:    "/home/linuxbrew/.linuxbrew/Cellar/tmux/3.7b/bin/tmux",
			Deleted: true,
		}, nil
	}
	c.serverVersion = func(context.Context, string) (string, error) { return "3.7b", nil }

	got := c.Run(&CheckContext{})
	if got.Status != StatusError {
		t.Fatalf("Status = %v, want %v", got.Status, StatusError)
	}
	joined := got.Message + "\n" + strings.Join(got.Details, "\n")
	for _, want := range []string{"deleted", "3.7c", "3.7b"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("result did not mention %q:\n%s", want, joined)
		}
	}
}

// TestTmuxServerBinaryCheckWarnsOnShadowedBinary covers the latent hazard: gc
// itself now pins one tmux, but a second one on PATH still means anything
// resolving tmux differently would hit the mismatch.
func TestTmuxServerBinaryCheckWarnsOnShadowedBinary(t *testing.T) {
	c := newTestTmuxServerBinaryCheck()
	c.pathCandidates = func() []string { return []string{"/usr/bin/tmux"} }
	c.clientVersion = func(_ context.Context, binary string) (string, error) {
		if binary == "/usr/bin/tmux" {
			return "3.4", nil
		}
		return "3.7c", nil
	}

	got := c.Run(&CheckContext{})
	if got.Status != StatusWarning {
		t.Fatalf("Status = %v, want %v (message: %s)", got.Status, StatusWarning, got.Message)
	}
	joined := strings.Join(got.Details, "\n")
	if !strings.Contains(joined, "/usr/bin/tmux") || !strings.Contains(joined, "3.4") {
		t.Fatalf("Details did not name the shadowed binary and its version:\n%s", joined)
	}
	if !strings.Contains(got.FixHint, sessiontmux.BinaryEnv) {
		t.Fatalf("FixHint = %q, want it to name %s", got.FixHint, sessiontmux.BinaryEnv)
	}
}

// TestTmuxServerBinaryCheckIgnoresTheResolvedClientAsItsOwnShadow guards the
// common install shape where a bin/ symlink and the versioned keg behind it
// are both reachable: they are one binary, not two.
func TestTmuxServerBinaryCheckIgnoresTheResolvedClientAsItsOwnShadow(t *testing.T) {
	dir := t.TempDir()
	keg := filepath.Join(dir, "tmux-real")
	if err := os.WriteFile(keg, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write keg: %v", err)
	}
	link := filepath.Join(dir, "tmux")
	if err := os.Symlink(keg, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	c := newTestTmuxServerBinaryCheck()
	c.clientBinary = func() string { return link }
	c.pathCandidates = func() []string { return []string{keg} }

	got := c.Run(&CheckContext{})
	if got.Status != StatusOK {
		t.Fatalf("Status = %v, want %v (message: %s)", got.Status, StatusOK, got.Message)
	}
}

func TestTmuxServerBinaryCheckWarnsWhenServerIdentityIsUnreadable(t *testing.T) {
	c := newTestTmuxServerBinaryCheck()
	c.inspectServer = func(context.Context, string) (*sessiontmux.ServerBinary, error) {
		return nil, errors.New("read peer pid: permission denied")
	}
	got := c.Run(&CheckContext{})
	if got.Status != StatusWarning {
		t.Fatalf("Status = %v, want %v (message: %s)", got.Status, StatusWarning, got.Message)
	}
}

func TestTmuxServerBinaryCheckDoesNotOfferToFix(t *testing.T) {
	// Restarting a tmux server drops every session it hosts, so this check
	// must never remediate on its own.
	if newTestTmuxServerBinaryCheck().CanFix() {
		t.Fatal("CanFix() = true, want false")
	}
}

func TestTmuxPathCandidatesDedupesAndSkipsNonExecutables(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	kegDir := filepath.Join(dir, "keg")
	emptyDir := filepath.Join(dir, "empty")
	for _, d := range []string{binDir, kegDir, emptyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	keg := filepath.Join(kegDir, "tmux")
	if err := os.WriteFile(keg, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write keg: %v", err)
	}
	if err := os.Symlink(keg, filepath.Join(binDir, "tmux")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	notExecutable := filepath.Join(emptyDir, "tmux")
	if err := os.WriteFile(notExecutable, []byte("not a program"), 0o644); err != nil {
		t.Fatalf("write non-executable: %v", err)
	}

	pathEnv := strings.Join([]string{binDir, kegDir, emptyDir, filepath.Join(dir, "missing")}, string(filepath.ListSeparator))
	got := tmuxPathCandidates(pathEnv)
	if len(got) != 1 {
		t.Fatalf("tmuxPathCandidates() = %#v, want exactly the one real binary", got)
	}
	if got[0] != filepath.Join(binDir, "tmux") {
		t.Fatalf("tmuxPathCandidates()[0] = %q, want the first PATH entry", got[0])
	}
}
