package rpc

import (
	"context"
	"fmt"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/courtapi"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/maximhq/bifrost/core/schemas"
)

// LoopbackCourt composes a *CourtClient with a direct reference
// to a local courtapi.Client implementation. Wire-safe methods are
// served through the Conn; non-wire-safe methods (GetMinister,
// ConfigureModel) go to Local.
//
// This shape is a stepping stone — it lets the TUI talk through a real
// msgpack codec for almost everything while keeping the last few
// hold-outs working in-process. Once those methods land over the wire
// (or are refactored away), LoopbackCourt collapses to the plain
// *CourtClient.
type LoopbackCourt struct {
	*CourtClient
	Local courtapi.Client
}

// NewLoopbackCourt wires a Conn-backed client that falls back to
// local for in-process-only methods.
func NewLoopbackCourt(conn *Conn, local courtapi.Client) *LoopbackCourt {
	return &LoopbackCourt{
		CourtClient: NewCourtClient(conn),
		Local:       local,
	}
}

// GetMinister delegates to the local court.
// Ministers carry non-wire-safe interface methods.
func (l *LoopbackCourt) GetMinister(id string) court.Minister {
	if l.Local == nil {
		return nil
	}
	return l.Local.GetMinister(id)
}

// ConfigureModel delegates to the local court. The LLM provider
// cannot cross a wire; this method stays in-process until bifrost
// initialisation moves to the daemon side.
func (l *LoopbackCourt) ConfigureModel(client court.LLMProvider, config *court.SessionConfig, repoInfo repo.RepoInfo) {
	if l.Local == nil {
		return
	}
	l.Local.ConfigureModel(client, config, repoInfo)
}

// CancellableStreamCtx is server-side machinery; the TUI never calls
// it through the LoopbackCourt. Delegate to Local so the interface
// is satisfied without minting orphan contexts on the client side.
func (l *LoopbackCourt) CancellableStreamCtx(channelID string) context.Context {
	if l.Local == nil {
		return context.Background()
	}
	return l.Local.CancellableStreamCtx(channelID)
}

// ListModels routes through the wire to the court that owns the LLM
// client. The local fallback exists only for a CourtClient-less
// LoopbackCourt.
func (l *LoopbackCourt) ListModels(provider string) (*schemas.BifrostListModelsResponse, error) {
	if l.CourtClient != nil {
		return l.CourtClient.ListModels(provider)
	}
	if l.Local == nil {
		return nil, fmt.Errorf("no local court available")
	}
	return l.Local.ListModels(provider)
}

// ApproximateContextSize routes through the wire to the court that owns
// the registry, mirroring ListModels. The local fallback exists only for a
// CourtClient-less LoopbackCourt.
func (l *LoopbackCourt) ApproximateContextSize(provider, model string) int {
	if l.CourtClient != nil {
		return l.CourtClient.ApproximateContextSize(provider, model)
	}
	if l.Local == nil {
		return 0
	}
	return l.Local.ApproximateContextSize(provider, model)
}

// ConnDone delegates to the underlying Conn's Done channel so the TUI
// can detect connection drops in loopback/daemon-socket mode.
func (l *LoopbackCourt) ConnDone() <-chan struct{} {
	return l.conn.Done()
}

var _ courtapi.Client = (*LoopbackCourt)(nil)
