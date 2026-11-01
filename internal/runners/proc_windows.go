//go:build windows

package runners

// processAlive cannot verify a process on Windows. Reporting alive keeps every
// container whose owner is unknown — fail safe, not loud — so reconciliation
// there is a no-op rather than a container killer.
func processAlive(int) bool {
	return true
}
