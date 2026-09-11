package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// claudeStateFileName is the Claude Code global state file. It is read from
// both the home directory and the Claude config directory because Claude Code
// honors CLAUDE_CONFIG_DIR for relocated state while older installs keep the
// file directly under $HOME.
const claudeStateFileName = ".claude.json"

// ClaudeStateFilePaths returns the Claude Code global state files that can
// record per-project workspace trust, in read order: the home-directory file
// first, then the file under configDir. Blank inputs are skipped and an
// overlapping configDir collapses to a single path, so the result is always
// deduplicated. Callers pass the result to ClaudeTrustedProjectRoots.
func ClaudeStateFilePaths(home, configDir string) []string {
	var paths []string
	seen := make(map[string]struct{}, 2)
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		path := filepath.Join(dir, claudeStateFileName)
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	add(home)
	add(configDir)
	return paths
}

// ClaudeTrustedProjectRoots returns the set of project paths that Claude Code
// has recorded an accepted workspace-trust prompt for, unioned across every
// readable state file in paths. Keys are cleaned so callers can compare them
// against a filepath.Clean'd repository root.
//
// A file that does not exist is not an error: it simply contributes nothing,
// since a Claude install that has never run records no trust at all. A file
// that exists but cannot be read or parsed is reported in the returned error
// while the roots gathered from the remaining files are still returned, so a
// caller can report the read failure without losing what it did learn.
func ClaudeTrustedProjectRoots(paths []string) (map[string]bool, error) {
	trusted := make(map[string]bool)
	var errs []error
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("reading Claude state %s: %w", path, err))
			}
			continue
		}
		var state struct {
			Projects map[string]json.RawMessage `json:"projects"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			errs = append(errs, fmt.Errorf("parsing Claude state %s: %w", path, err))
			continue
		}
		for project, raw := range state.Projects {
			project = strings.TrimSpace(project)
			if project == "" {
				continue
			}
			// hasTrustDialogAccepted is the per-project field Claude Code
			// sets once a human has accepted the workspace-trust prompt.
			var entry struct {
				TrustAccepted bool `json:"hasTrustDialogAccepted"`
			}
			// A project entry that is not an object (or omits the field)
			// records no acceptance; an unexpected shape is not a state-file
			// error because Claude Code owns this schema and may extend it.
			if err := json.Unmarshal(raw, &entry); err != nil {
				continue
			}
			if entry.TrustAccepted {
				trusted[filepath.Clean(project)] = true
			}
		}
	}
	return trusted, errors.Join(errs...)
}
