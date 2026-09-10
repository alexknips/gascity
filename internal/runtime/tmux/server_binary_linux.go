//go:build linux

package tmux

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// deletedExeSuffix is what Linux appends to /proc/<pid>/exe once the
// executable's directory entry is gone. The kernel keeps the inode alive for
// the running process, so the link still resolves — it just names a path
// nothing occupies any more.
const deletedExeSuffix = " (deleted)"

// processExePath resolves pid's executable via /proc and reports whether the
// file backing it has been unlinked.
func processExePath(pid int) (path string, deleted bool, err error) {
	link := "/proc/" + strconv.Itoa(pid) + "/exe"
	target, err := os.Readlink(link)
	if err != nil {
		return "", false, fmt.Errorf("readlink %s: %w", link, err)
	}
	if trimmed := strings.TrimSuffix(target, deletedExeSuffix); trimmed != target {
		return trimmed, true, nil
	}
	return target, false, nil
}
