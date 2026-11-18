package daemon

import (
	"net"
	"os/user"
	"strconv"
	"syscall"
)

// unixPeerCredential reads the kernel-verified peer credential (pid, uid) of a
// Unix socket. It borrows the live fd through SyscallConn + Control for the
// duration of the getsockopt: (*net.UnixConn).File would dup the fd and put it
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

	var ucred *syscall.Ucred
	if err := raw.Control(func(fd uintptr) {
		ucred, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || ucred == nil {
		return 0, 0, false
	}
	return int(ucred.Pid), int(ucred.Uid), true
}

// unixPeerUsername extracts the verified username from a Unix socket
// peer credential (SO_PEERCRED on Linux).
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
// socket (SO_PEERCRED on Linux), or 0 when it cannot be established. The
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
