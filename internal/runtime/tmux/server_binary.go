package tmux

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// ErrNoServerSocket reports that no tmux server is listening on the named
// socket. It is the normal, healthy answer on a host where the city is not
// running, and callers should treat it as "nothing to inspect" rather than a
// failure.
var ErrNoServerSocket = errors.New("no tmux server socket")

// ServerBinary describes the executable behind a running tmux server.
type ServerBinary struct {
	// PID is the server process id, read from the socket's peer credentials.
	PID int
	// Path is the server executable's path. On a server whose binary was
	// replaced or removed since it started, this is the path the file used to
	// occupy — the running image is no longer reachable there.
	Path string
	// Deleted is true when the server's executable no longer exists at Path.
	// A package upgrade that replaces a keg leaves the old server running an
	// unlinked inode: it keeps working, but it cannot be re-executed, and it
	// can only be moved onto the live binary by restarting the server and
	// dropping every session it hosts.
	Deleted bool
}

// serverBinaryDialTimeout bounds the socket connect. A tmux server that
// accepts but never completes the handshake must not hold a doctor check.
const serverBinaryDialTimeout = 2 * time.Second

// InspectServerBinary identifies the executable running the tmux server bound
// to socketName, without speaking the tmux protocol to it. It connects to the
// server's unix socket, reads the peer credentials to learn the server pid,
// and resolves that pid's executable.
//
// Reading the binary out of band matters: a client whose version does not
// match the server cannot ask the server anything, so any answer that depends
// on the tmux protocol is unavailable in exactly the case worth diagnosing.
//
// It returns [ErrNoServerSocket] when nothing is listening.
func InspectServerBinary(ctx context.Context, socketName string) (*ServerBinary, error) {
	if socketName == "" {
		return nil, fmt.Errorf("%w: empty socket name", ErrNoServerSocket)
	}
	path := namedSocketPath(socketName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoServerSocket, path)
	}
	if err != nil {
		return nil, fmt.Errorf("stat tmux socket %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("%s is not a unix socket", path)
	}

	dialCtx, cancel := context.WithTimeout(ctx, serverBinaryDialTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", path)
	if err != nil {
		// A socket file left behind by a dead server refuses connections.
		// That is an absent server, not a broken one.
		return nil, fmt.Errorf("%w: %s: %w", ErrNoServerSocket, path, err)
	}
	defer func() { _ = conn.Close() }()

	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("unexpected connection type %T for %s", conn, path)
	}
	pid, err := socketPeerPID(unixConn)
	if err != nil {
		return nil, fmt.Errorf("read peer pid for %s: %w", path, err)
	}

	exePath, deleted, err := processExePath(pid)
	if err != nil {
		return nil, fmt.Errorf("resolve executable for tmux server pid %d: %w", pid, err)
	}
	return &ServerBinary{PID: pid, Path: exePath, Deleted: deleted}, nil
}
