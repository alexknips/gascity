package main

import (
	"fmt"
	"strconv"
	"strings"
)

// rigAddLocalArgsRefusal reports whether a local `gc rig add` invocation carries
// more positional arguments than the command takes, and the message to print.
// refused is false for the documented zero- or one-argument forms.
//
// The command takes one positional — the rig path — and carries the rig name on
// --name, but `gc rig add <name> <path>` reads naturally enough that operators
// write it. Cobra accepts arbitrary args here (the remote path refuses every
// positional with its own text), so the extras used to be dropped without a
// word and args[0] became the path. A bare name is relative, and a relative rig
// argument resolves against the city, so the add provisioned $GC_CITY/<name>:
// a stray directory registered as the rig root, leaving the rig unable to do
// any git work and recovery a hand-edit of city.toml (ga-lyl).
//
// The message echoes every argument because the operator cannot otherwise see
// which one was taken as the path, and names the working form so the fix does
// not require rereading --help.
func rigAddLocalArgsRefusal(args []string) (message string, refused bool) {
	if len(args) <= 1 {
		return "", false
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, strconv.Quote(arg))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "gc rig add: takes one positional argument (the rig path); got %d: %s",
		len(args), strings.Join(quoted, " "))
	b.WriteString("\n  the rig name is a flag, not a positional: ")
	if len(args) == 2 {
		// The two-argument case is the reported misreading, so name the exact
		// command that does what the operator meant rather than a placeholder.
		fmt.Fprintf(&b, "gc rig add %s --name %s", args[1], args[0])
	} else {
		b.WriteString("gc rig add <path> --name <name>")
	}
	return b.String(), true
}
