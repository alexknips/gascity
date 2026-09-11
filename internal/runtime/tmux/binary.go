package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// BinaryEnv names the environment variable that pins the tmux executable gc
// invokes. Set it to an absolute path to take PATH out of the loop entirely.
//
// Why this exists: tmux clients and servers speak a private protocol that is
// only guaranteed compatible within a version. A host with more than one tmux
// on PATH (a distro /usr/bin/tmux alongside a Homebrew keg, say) will drive
// the same server with different clients depending on which PATH a control
// path inherited, and the mismatched client fails with "server exited
// unexpectedly". Pinning one executable makes that impossible for gc.
const BinaryEnv = "GC_TMUX_BIN"

// DefaultBinary is the command name resolved through PATH when BinaryEnv is
// unset. It is also the last-resort return value: if PATH lookup fails there
// is nothing better to hand exec, and the resulting error names tmux the way
// callers expect.
const DefaultBinary = "tmux"

// Binary returns the tmux executable that every gc-managed invocation must
// use: the BinaryEnv pin when it is set, and the first tmux on PATH
// otherwise. Callers must not fall back to the bare "tmux" string — doing so
// reintroduces the split-resolution failure this function exists to prevent.
//
// It answers from the environment on every call and deliberately keeps no
// cache. Resolving is a handful of stat calls and every caller is about to
// fork/exec tmux, so memoizing saves nothing measurable — while a remembered
// absolute path goes permanently wrong the moment it stops naming a file. A
// package upgrade that unlinks a keg under a long-lived supervisor or mayor
// process would turn every later tmux call in it into fork/exec ENOENT, with
// no recovery short of restarting the process. That is the same dead-keg
// failure this binary pinning exists to catch, reintroduced one layer up, and
// it is strictly worse than resolving again: the deleted path cannot work,
// and PATH may well name a live tmux.
//
// Consistency comes from the environment instead, which is where it is
// actually enforceable. Resolution is deterministic in its inputs, so a
// process whose environment holds still sees one stable answer, and nothing
// in gc rewrites its own PATH. For a guarantee that spans every control path,
// including the ones gc does not own, pin BinaryEnv; the tmux-server-binary
// doctor check reports a second tmux on PATH so an operator knows when that
// is worth doing.
func Binary() string {
	return resolveBinary(os.Getenv, exec.LookPath)
}

// resolveBinary holds the resolution policy. Binary passes the real os and
// exec lookups; taking them as parameters lets the policy be exercised
// without touching the process environment.
func resolveBinary(getenv func(string) string, lookPath func(string) (string, error)) string {
	if pinned := strings.TrimSpace(getenv(BinaryEnv)); pinned != "" {
		return pinned
	}
	if resolved, err := lookPath(DefaultBinary); err == nil && resolved != "" {
		return resolved
	}
	return DefaultBinary
}

// versionProbeTimeout bounds both version probes. `tmux -V` is local and
// instant; the server probe talks to the server, which is the thing that may
// be wedged, so neither may run unbounded.
const versionProbeTimeout = 5 * time.Second

// BinaryVersion returns the version string reported by a tmux executable,
// e.g. "3.7c". It runs `tmux -V`, which does not contact any server: it
// answers even when the server is unreachable, and probing a binary this way
// cannot disturb a server that binary is not meant to drive.
func BinaryVersion(ctx context.Context, binary string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, binary, "-V").Output()
	if err != nil {
		return "", fmt.Errorf("%s -V: %w", binary, err)
	}
	version := parseVersionOutput(string(out))
	if version == "" {
		return "", fmt.Errorf("%s -V returned no version: %q", binary, strings.TrimSpace(string(out)))
	}
	return version, nil
}

// parseVersionOutput extracts the version from `tmux -V` output, which is the
// single line "tmux <version>".
func parseVersionOutput(out string) string {
	fields := strings.Fields(out)
	if len(fields) < 2 || fields[0] != DefaultBinary {
		return ""
	}
	return fields[1]
}

// ServerVersion asks the tmux server bound to socketName which version it is,
// via the client's #{version} format. The answer is the server's version, not
// the client's, which is what makes a mismatch visible.
//
// An error here is itself diagnostic: a client too far from the server to
// speak its protocol reports "server exited unexpectedly", which is the
// crash-loop symptom.
func ServerVersion(ctx context.Context, socketName string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()
	args := []string{}
	if socketName != "" {
		args = append(args, "-L", socketName)
	}
	args = append(args, "display-message", "-p", "#{version}")
	out, err := exec.CommandContext(probeCtx, Binary(), args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
				return "", fmt.Errorf("tmux -L %s display-message: %w: %s", socketName, err, stderr)
			}
		}
		return "", fmt.Errorf("tmux -L %s display-message: %w", socketName, err)
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", fmt.Errorf("tmux -L %s reported an empty version", socketName)
	}
	return version, nil
}
