//go:build darwin

package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// processExePath resolves pid's executable on macOS, where there is no /proc.
// `ps -o comm=` reports the full executable path for a process started from
// an absolute path, which is how gc spawns the tmux server.
//
// The deleted-binary signal is weaker here than on Linux: macOS reports the
// path the process was launched from with no marker when that path has since
// been replaced or removed. Stat the path to recover the same fact — an
// executable that is gone from disk is the condition worth reporting, and a
// path that was replaced in place is indistinguishable from one that was not,
// so a caller must not read deleted=false as proof the running image matches
// what sits at Path.
func processExePath(pid int) (path string, deleted bool, err error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "", false, fmt.Errorf("ps -p %d -o comm=: %w", pid, err)
	}
	target := strings.TrimSpace(string(out))
	if target == "" {
		return "", false, fmt.Errorf("ps reported no command for pid %d", pid)
	}
	if !strings.HasPrefix(target, "/") {
		// A bare command name carries no path to check.
		return target, false, nil
	}
	if _, statErr := os.Stat(target); statErr != nil && os.IsNotExist(statErr) {
		return target, true, nil
	}
	return target, false, nil
}
