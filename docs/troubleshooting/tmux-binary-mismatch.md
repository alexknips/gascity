---
title: Resolve a tmux Client/Server Binary Mismatch
description: Diagnose the tmux-server-binary doctor check and restart a Gas City tmux server without silently dropping every session.
---

## Overview

Gas City drives agent sessions through a single tmux server, one per city,
reached over a named socket. tmux clients and servers speak a private protocol
with no cross-version compatibility guarantee. A client from a different build
than the server connects, fails to negotiate, and reports:

```
server exited unexpectedly
```

The message names the wrong party. The server is fine; the client could not
talk to it. Every gc control path that resolved a different tmux than the one
running the server sees the same thing, so the symptom looks like a dead city
while `tmux list-sessions` from your own shell works perfectly.

Two conditions produce it:

1. **More than one tmux on PATH.** A distro `/usr/bin/tmux` alongside a
   Homebrew or Nix build is the common shape. Which one a control path gets
   depends on the PATH it inherited, and a systemd unit, a cron entry, and an
   interactive shell rarely agree.
2. **The server's own binary was replaced underneath it.** A package upgrade
   unlinks the executable the running server started from. The server keeps
   running the old inode and keeps working. It cannot be re-executed, and it
   cannot be moved onto the new build without a restart.

## How gc pins the client

Every gc-managed tmux invocation goes through one resolution point rather than
naming `tmux` and letting each call site inherit whatever PATH it was handed.
That includes the hidden-attach path, which runs under a shell whose PATH gc
does not control.

Resolution reads the environment on each call, so it always reflects the
environment the process is actually in: gc will not go on execing a path that
a package upgrade has since unlinked. The corollary is that PATH decides, and
PATH is not something gc can vouch for. Set `GC_TMUX_BIN` to an absolute path
to take PATH out of the decision entirely:

```bash
export GC_TMUX_BIN=/home/linuxbrew/.linuxbrew/bin/tmux
```

Pin it in the environment the controller and supervisor inherit, not only in
your shell. A pin that only your terminal sees leaves every background control
path resolving through PATH.

## Symptoms

1. `gc doctor` reports `tmux-server-binary` as **Warning** or **Error**.
   The older `tmux-binary` check only resolves PATH, so it passes in every case
   below.
2. `gc session list`, `gc session nudge`, or session reconciliation fails with
   `server exited unexpectedly` while the same command works from your shell.
3. A specific tmux fails against the city socket while another succeeds:

   ```bash
   /usr/bin/tmux -L gc list-sessions                       # server exited unexpectedly
   /home/linuxbrew/.linuxbrew/bin/tmux -L gc list-sessions  # lists sessions
   ```

## Diagnose

Ask the server its own version through a client that can reach it. The
`#{version}` format reports the **server's** version, not the client's:

```bash
tmux -V                                          # client version
tmux -L <socket> display-message -p '#{version}'  # server version
```

Find the server process and the executable behind it. On Linux the `(deleted)`
suffix is the kernel telling you the file is gone:

```bash
SERVER_PID=$(pgrep -f "tmux .*-L <socket>" | head -1)
readlink /proc/"$SERVER_PID"/exe
# /home/linuxbrew/.linuxbrew/Cellar/tmux/3.7b/bin/tmux (deleted)
```

List every tmux PATH can reach, with its version. `tmux -V` never contacts a
server, so this is safe to run against a live city:

```bash
for dir in $(printf '%s\n' "$PATH" | tr ':' '\n'); do
  [ -x "$dir/tmux" ] && printf '%s: %s\n' "$dir/tmux" "$("$dir/tmux" -V)"
done
```

The city socket lives at `${TMUX_TMPDIR:-/tmp}/tmux-$(id -u)/<socket>`, where
`<socket>` is `session.socket` from `city.toml` or the city name when unset.

## Fix without restarting

A version mismatch caused by PATH alone needs no restart. Pin `GC_TMUX_BIN` to
the build the server is running and restart only the gc processes that resolved
the wrong one:

```bash
systemctl --user restart gascity-supervisor
```

Confirm with `gc doctor`. This is the whole fix whenever the server's own
executable still exists on disk.

## Restart the tmux server

**A tmux server restart kills every session it hosts.** There is no reload, no
re-exec, and no way to migrate live panes onto a new binary. Agents lose their
panes and their in-flight work. Do not do this to tidy up a `(deleted)`
executable that is otherwise working — that state is stable, and the doctor
check reports it as advisory precisely so it does not gate anything. Schedule
it.

Read this before starting: if you are working inside a session on this server,
the restart kills your own session mid-procedure. Run it from a shell outside
tmux, or from a different host.

1. **Announce the window.** Every agent in the city is about to lose its pane.

2. **Record what is running,** so you can tell afterwards what came back:

   ```bash
   gc session list > /tmp/sessions-before.txt
   tmux -L <socket> list-sessions >> /tmp/sessions-before.txt
   ```

3. **Drain the agents.** Let each one reach a handoff point and persist its
   state to beads rather than killing it mid-tool-call. Polecats hand their
   branch to the refinery; the refinery finishes or abandons the merge it
   holds. Watch `gc session list` until the sessions you drained are gone.

4. **Stop the controller and supervisor** so nothing respawns a session into
   the server you are about to kill:

   ```bash
   gc stop
   systemctl --user stop gascity-supervisor
   ```

   `gc stop` also stops the deacon. Nothing will run town patrols until you
   start it again.

5. **Confirm the server is empty,** then kill it:

   ```bash
   tmux -L <socket> list-sessions   # expect "no server running" or an empty list
   tmux -L <socket> kill-server
   ```

   A non-empty list means a session survived the drain. Go back to step 3 —
   killing the server now discards that agent's work.

6. **Verify the stale socket is gone.** A socket file left behind refuses
   connections and gc treats it as an absent server, but remove it if the
   server did not:

   ```bash
   ls -l "${TMUX_TMPDIR:-/tmp}/tmux-$(id -u)/<socket>"
   ```

7. **Confirm the binary that will start the next server** is the one you
   intend. This is the step the whole procedure exists for:

   ```bash
   echo "${GC_TMUX_BIN:-$(command -v tmux)}"
   "${GC_TMUX_BIN:-$(command -v tmux)}" -V
   ```

8. **Start the city again:**

   ```bash
   systemctl --user start gascity-supervisor
   gc start
   ```

9. **Verify.** The new server's executable should exist on disk, and its
   version should match the client:

   ```bash
   gc doctor          # tmux-server-binary passes
   gc session list    # sessions come back
   ```

## Verify the outcome

`tmux-server-binary` passes when the client gc invokes and the running server
report the same version and the server's executable still exists. It warns when
they match but a second tmux is reachable through PATH — gc itself is pinned,
but anything driving the socket from outside gc can still hit the mismatch.

## Related

- `gc doctor` check `tmux-binary` — resolves PATH only. It cannot see either
  condition on this page, which is why `tmux-server-binary` exists.
- [Recover from Dolt Bloat](/troubleshooting/dolt-bloat-recovery) — the other
  procedure whose remediation must be scheduled rather than run on sight.
