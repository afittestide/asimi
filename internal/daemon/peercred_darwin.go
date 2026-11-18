package daemon

import (
	"net"
	"os/user"
	"strconv"

	"golang.org/x/sys/unix"
)

// unixPeerCredential reads the kernel-verified peer credential (pid, uid) of a
// Unix socket. It borrows the live fd through SyscallConn + Control for the
// duration of the getsockopts: (*net.UnixConn).File would dup the fd and put it
// in blocking mode, taking the connection off the netpoller on every
// handshake. ok is false when no credential could be read.
func unixPeerCredential(conn net.Conn) (pid, uid int, ok bool) {
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return 0, 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}

	var xucred *unix.Xucred
	var peerPID int
	controlErr := raw.Control(func(fd uintptr) {
		xucred, _ = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		// macOS exposes the peer's uid in the LOCAL_PEERCRED credential but
		// not its PID: that needs a second getsockopt (LOCAL_PEERPID).
		peerPID, _ = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if controlErr != nil || xucred == nil {
		return 0, 0, false
	}
	return peerPID, int(xucred.Uid), true
}

// unixPeerUsername extracts the verified username from a Unix socket
// peer credential (getsockopt with SOL_LOCAL/LOCAL_PEERCRED on macOS).
func unixPeerUsername(conn net.Conn) string {
	_, uid, ok := unixPeerCredential(conn)
	if !ok {
		return ""
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return ""
	}
	return u.Username
}

// unixPeerPID returns the PID of the process at the other end of a Unix
// socket (LOCAL_PEERPID on macOS), or 0 when it cannot be established. The
// mandate is owned by this PID: it is the client that established the
// session, so it must be the same process the sandbox container is stamped
// with (edict 1036).
func unixPeerPID(conn net.Conn) int {
	pid, _, ok := unixPeerCredential(conn)
	if !ok {
		return 0
	}
	return pid
}
