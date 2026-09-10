//go:build !linux && !darwin

package tmux

import "fmt"

// processExePath has no portable implementation outside linux and darwin.
func processExePath(pid int) (string, bool, error) {
	return "", false, fmt.Errorf("executable lookup for pid %d is unsupported on this platform", pid)
}
