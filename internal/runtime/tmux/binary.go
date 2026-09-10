package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
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

var (
	binaryOnce sync.Once
	binaryPath string
)

// Binary returns the tmux executable that every gc-managed invocation must
// use. It resolves once per process and caches the result, so a PATH change
// after the first call — a re-exec'd shell, a supervisor restart writing a
// different environment — cannot make a later gc call reach a different tmux
// than the one that started the server.
//
// Callers must not fall back to the bare "tmux" string. Doing so reintroduces
// the split-resolution failure this function exists to prevent.
func Binary() string {
	binaryOnce.Do(func() {
		binaryPath = resolveBinary(os.Getenv, exec.LookPath)
	})
	return binaryPath
}

// resolveBinary holds the resolution policy, separated from the cache so it
// can be exercised directly.
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
