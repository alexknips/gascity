package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sessiontmux "github.com/gastownhall/gascity/internal/runtime/tmux"
)

// TmuxServerBinaryCheck verifies that the tmux client gc invokes and the tmux
// server it drives are the same build, and that the server's executable still
// exists on disk.
//
// It exists because the plain "is tmux on PATH" check cannot see either
// failure. tmux clients and servers speak a private protocol with no
// cross-version compatibility guarantee: a client from a different build
// reaches the server and fails with "server exited unexpectedly", which reads
// as a dead server rather than a mismatched client. Meanwhile a PATH lookup
// succeeds and reports a healthy tmux, so a check that only resolves PATH
// passes while the exact failure it was written for is live.
//
// The second condition is quieter and harder to recover from. A package
// upgrade that replaces a keg unlinks the executable the running server was
// started from. The server keeps running the old inode and keeps working, but
// it can never be moved onto the new build without being restarted, and
// restarting a tmux server drops every session it hosts. Naming that state
// while it is still benign is the only cheap moment to act on it.
type TmuxServerBinaryCheck struct {
	socketName string

	// Seams. All are set to real implementations by the constructor and
	// replaced in tests so the check can be exercised without a tmux server.
	clientBinary   func() string
	clientVersion  func(context.Context, string) (string, error)
	serverVersion  func(context.Context, string) (string, error)
	inspectServer  func(context.Context, string) (*sessiontmux.ServerBinary, error)
	pathCandidates func() []string
}

// NewTmuxServerBinaryCheck creates the check for the given tmux socket name.
// An empty socketName means the city drives tmux's default server.
func NewTmuxServerBinaryCheck(socketName string) *TmuxServerBinaryCheck {
	return &TmuxServerBinaryCheck{
		socketName:    socketName,
		clientBinary:  sessiontmux.Binary,
		clientVersion: sessiontmux.BinaryVersion,
		serverVersion: sessiontmux.ServerVersion,
		inspectServer: sessiontmux.InspectServerBinary,
		pathCandidates: func() []string {
			return tmuxPathCandidates(os.Getenv("PATH"))
		},
	}
}

// Name returns the check identifier.
func (c *TmuxServerBinaryCheck) Name() string { return "tmux-server-binary" }

// CanFix reports false: every remediation here either repins configuration or
// restarts the tmux server, and restarting drops live sessions. That is an
// operator decision, not something a doctor run should take on its own.
func (c *TmuxServerBinaryCheck) CanFix() bool { return false }

// Fix is never called because CanFix returns false.
func (c *TmuxServerBinaryCheck) Fix(*CheckContext) error { return nil }

// WarmupEligible reports false. The check dials the tmux socket and execs
// tmux, which is more than `gc start`'s warm-up scan should do.
func (c *TmuxServerBinaryCheck) WarmupEligible() bool { return false }

// Run compares the resolved client against the running server.
//
// Every failure it reports is advisory. The conditions are real, but each one
// is remediated by repinning configuration or by an operator-scheduled server
// restart, so gating dispatch on them would stop the city for something the
// city cannot fix on its own.
func (c *TmuxServerBinaryCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name(), Severity: SeverityAdvisory}
	ctx := context.Background()

	client := c.clientBinary()
	server, err := c.inspectServer(ctx, c.socketName)
	if errors.Is(err, sessiontmux.ErrNoServerSocket) {
		// Nothing is listening where the socket should be. Before calling
		// that healthy, ask the client whether it can reach a server
		// anyway: if it can, this check looked in the wrong place, and
		// reporting a pass would be the same "absent result reads as
		// proof of absence" mistake the check exists to catch.
		if version, probeErr := c.serverVersion(ctx, c.socketName); probeErr == nil {
			r.Status = StatusWarning
			r.Message = fmt.Sprintf(
				"no tmux socket found for %s, but the client reached a server on it (tmux %s); "+
					"this check cannot compare binaries", c.socketLabel(), version)
			r.Details = []string{fmt.Sprintf("socket lookup: %v", err)}
			r.FixHint = "TMUX_TMPDIR likely differs between the tmux server and this process"
			return r
		}
		r.Status = StatusOK
		r.Message = fmt.Sprintf("no tmux server on socket %s; client is %s", c.socketLabel(), client)
		r.Details = []string{fmt.Sprintf("socket lookup: %v", err)}
		return r
	}
	if err != nil {
		// The server is listening but its identity could not be read.
		// Report it rather than passing: an unreadable server is exactly
		// the state this check must not gloss over.
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("could not identify the tmux server on socket %s: %v", c.socketLabel(), err)
		r.FixHint = "re-run with --verbose; if the server is wedged, capture diagnostics before restarting it"
		return r
	}

	var problems []string
	details := []string{
		fmt.Sprintf("client binary: %s", client),
		fmt.Sprintf("server pid %d binary: %s", server.PID, server.Path),
	}

	if server.Deleted {
		problems = append(problems, fmt.Sprintf(
			"tmux server pid %d is running a deleted executable (%s)", server.PID, server.Path))
	}

	clientVer, clientErr := c.clientVersion(ctx, client)
	if clientErr != nil {
		details = append(details, fmt.Sprintf("client version: unavailable (%v)", clientErr))
	} else {
		details = append(details, "client version: "+clientVer)
	}

	serverVer, serverErr := c.serverVersion(ctx, c.socketName)
	switch {
	case serverErr != nil:
		// The client could not get an answer out of a server that is
		// demonstrably listening — the mismatch symptom itself.
		problems = append(problems, fmt.Sprintf(
			"the resolved tmux client (%s) cannot query the running server: %v", client, serverErr))
	case clientErr == nil && clientVer != serverVer:
		problems = append(problems, fmt.Sprintf(
			"tmux client is %s but the server on socket %s is %s", clientVer, c.socketLabel(), serverVer))
		details = append(details, "server version: "+serverVer)
	default:
		details = append(details, "server version: "+serverVer)
	}

	shadows := c.shadowBinaries(ctx, client)
	details = append(details, shadows...)

	if len(problems) > 0 {
		r.Status = StatusError
		r.Message = problems[0]
		if len(problems) > 1 {
			r.Message = fmt.Sprintf("%s (+%d more)", problems[0], len(problems)-1)
		}
		r.Details = append(problems[1:], details...)
		r.FixHint = tmuxServerBinaryFixHint
		return r
	}

	if len(shadows) > 0 {
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("client and server both %s, but another tmux is on PATH", serverVer)
		r.Details = details
		r.FixHint = fmt.Sprintf(
			"pin the tmux gc invokes with %s=%s so no control path can resolve a different build",
			sessiontmux.BinaryEnv, client)
		return r
	}

	r.Status = StatusOK
	r.Message = fmt.Sprintf("client and server both tmux %s (%s)", serverVer, client)
	r.Details = details
	return r
}

const tmuxServerBinaryFixHint = "pin the client with " + sessiontmux.BinaryEnv + "; moving the server onto a different " +
	"binary requires restarting it, which kills every session it hosts — follow " +
	"docs/troubleshooting/tmux-binary-mismatch.md before doing that"

func (c *TmuxServerBinaryCheck) socketLabel() string {
	if c.socketName == "" {
		return "(default)"
	}
	return c.socketName
}

// shadowBinaries lists other tmux executables on PATH, with their versions.
// They are reported rather than failed on: once gc pins one binary, a second
// tmux on PATH is a latent hazard for anything driving the server outside gc,
// not a live defect in gc itself.
func (c *TmuxServerBinaryCheck) shadowBinaries(ctx context.Context, client string) []string {
	clientReal := realPath(client)
	var out []string
	for _, candidate := range c.pathCandidates() {
		if realPath(candidate) == clientReal {
			continue
		}
		version, err := c.clientVersion(ctx, candidate)
		if err != nil {
			out = append(out, fmt.Sprintf("also on PATH: %s (version unavailable: %v)", candidate, err))
			continue
		}
		out = append(out, fmt.Sprintf("also on PATH: %s (tmux %s)", candidate, version))
	}
	sort.Strings(out)
	return out
}

// tmuxPathCandidates returns each distinct tmux executable reachable through
// the given PATH value, in PATH order.
func tmuxPathCandidates(pathEnv string) []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, sessiontmux.DefaultBinary)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		real := realPath(candidate)
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, candidate)
	}
	return out
}

// realPath resolves symlinks so a versioned keg and the bin/ symlink pointing
// at it compare equal. It falls back to a cleaned path when resolution fails.
func realPath(path string) string {
	if path == "" {
		return ""
	}
	if !strings.Contains(path, string(filepath.Separator)) {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
