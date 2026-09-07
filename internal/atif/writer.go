package atif

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// AtifWriter manages JSONL output files for ATIF trajectory recording.
// It writes to two locations:
//   - agent/<agent_name>.txt (aggregated JSONL)
//   - agent/<agent_name>/sessions/<session_id>/<session_id>.jsonl (per-session JSONL)
//
// Output paths are rooted at projectRoot so recording stays correct even when
// the OS working directory changes (e.g. ritual step sessions run with OS CWD
// under .../court). When projectRoot is empty, paths fall back to the current
// working directory.
type AtifWriter struct {
	mu          sync.Mutex
	aggFile     *os.File
	sessFile    *os.File
	agentName   string
	sessionID   string
	projectRoot string
	aggPath     string
	sessPath    string
}

// NewAtifWriter creates an AtifWriter for the given agent name and session ID.
// Output files are rooted under projectRoot so recording tracks the canonical
// project rather than the transient OS working directory. If projectRoot is
// empty, the current working directory is used. If the agent/ directory cannot
// be created, Open logs a warning and the writer stays closed (non-fatal).
//
// When ASIMI_HOME is set (containerized drivers like Harbor persist it), the
// per-session output is routed to ${ASIMI_HOME}/sessions/<sessionID>/<sessionID>.jsonl
// so external drivers can locate the trajectory files. Harbor's driver scans
// sessions/ for one subdirectory per session, so the .jsonl is nested inside
// the per-session directory rather than being written directly under sessions/.
// The aggregated file stays at projectRoot.
func NewAtifWriter(agentName, sessionID, projectRoot string) *AtifWriter {
	root := projectRoot
	if root == "" {
		root, _ = os.Getwd()
	}
	aggPath := filepath.Join(root, "agent", agentName+".txt")
	sessPath := filepath.Join(root, "agent", agentName, "sessions", sessionID+".jsonl")

	// Honor ASIMI_HOME: when set (containerized drivers like Harbor persist it),
	// route per-session output to ${ASIMI_HOME}/sessions/<sessionID>/ so external
	// drivers can locate the trajectory files. Harbor's _get_session_dir() scans
	// sessions/ for one subdirectory per session, so the .jsonl is nested inside
	// the per-session directory. The aggregated file stays at project root.
	if home := os.Getenv("ASIMI_HOME"); home != "" {
		sessPath = filepath.Join(home, "sessions", sessionID, sessionID+".jsonl")
	}

	w := &AtifWriter{
		agentName:   agentName,
		sessionID:   sessionID,
		projectRoot: root,
		aggPath:     aggPath,
		sessPath:    sessPath,
	}
	return w
}

// Open creates the agent/ directories and opens both output files.
// If directory creation fails, it logs a warning and returns nil (non-fatal).
func (w *AtifWriter) Open() {
	if w == nil {
		return
	}

	// Ensure both parent directories exist. sessPath may be routed under
	// ${ASIMI_HOME}/sessions while aggPath stays under root/agent, so both
	// must be created explicitly (MkdirAll is a no-op when already present).
	aggDir := filepath.Dir(w.aggPath)
	if err := os.MkdirAll(aggDir, 0755); err != nil {
		slog.Warn("atif: failed to create directory, disabling trajectory recording",
			"dir", aggDir, "error", err)
		return
	}
	dir := filepath.Dir(w.sessPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		slog.Warn("atif: failed to create directory, disabling trajectory recording",
			"dir", dir, "error", err)
		return
	}

	// Open aggregated file
	aggFile, err := os.OpenFile(w.aggPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Warn("atif: failed to open aggregated trajectory file, disabling recording",
			"path", w.aggPath, "error", err)
		return
	}

	// Open per-session file
	sessFile, err := os.OpenFile(w.sessPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		aggFile.Close()
		slog.Warn("atif: failed to open session trajectory file, disabling recording",
			"path", w.sessPath, "error", err)
		return
	}

	w.aggFile = aggFile
	w.sessFile = sessFile
	slog.Debug("atif: trajectory recording started",
		"projectRoot", w.projectRoot,
		"aggregated", w.aggPath,
		"session", w.sessPath)
}

// WriteEvent appends a single JSON line to both output files.
// Errors are logged but not returned (non-fatal).
func (w *AtifWriter) WriteEvent(v any) {
	if w == nil || w.aggFile == nil || w.sessFile == nil {
		return
	}

	data, err := json.Marshal(v)
	if err != nil {
		slog.Warn("atif: failed to marshal event", "error", err)
		return
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.aggFile.Write(data); err != nil {
		slog.Warn("atif: failed to write to aggregated file", "error", err)
	}
	if _, err := w.sessFile.Write(data); err != nil {
		slog.Warn("atif: failed to write to session file", "error", err)
	}
}

// Flush flushes both output files.
func (w *AtifWriter) Flush() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.aggFile != nil {
		w.aggFile.Sync()
	}
	if w.sessFile != nil {
		w.sessFile.Sync()
	}
}

// Close flushes and closes both output files.
func (w *AtifWriter) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	var errs []error
	if w.aggFile != nil {
		w.aggFile.Sync()
		if err := w.aggFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close aggregated file: %w", err))
		}
		w.aggFile = nil
	}
	if w.sessFile != nil {
		w.sessFile.Sync()
		if err := w.sessFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close session file: %w", err))
		}
		w.sessFile = nil
	}
	for _, err := range errs {
		slog.Warn("atif: error closing files", "error", err)
	}
}

// IsOpen returns true if the writer has successfully opened files.
func (w *AtifWriter) IsOpen() bool {
	return w != nil && w.aggFile != nil && w.sessFile != nil
}
