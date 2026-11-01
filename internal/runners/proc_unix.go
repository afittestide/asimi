//go:build !windows

package runners

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid is a live process. EPERM counts as alive:
// the process exists but belongs to another user.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
