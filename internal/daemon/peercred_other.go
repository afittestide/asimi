//go:build !linux && !darwin

package daemon

import (
	"net"
)

// unixPeerCredential reports that no peer credential is available on this
// platform, so the mandate owner cannot be resolved from the socket.
func unixPeerCredential(net.Conn) (pid, uid int, ok bool) {
	return 0, 0, false
}

// unixPeerUsername extracts the verified username from a Unix socket
// peer credential. This fallback returns "" on unsupported platforms.
func unixPeerUsername(conn net.Conn) string {
	return ""
}

// unixPeerPID returns the PID of the process at the other end of a Unix
// socket. This fallback returns 0 (unknown) on unsupported platforms; the
// mandate then falls back to the Court's own PID.
func unixPeerPID(conn net.Conn) int {
	return 0
}
