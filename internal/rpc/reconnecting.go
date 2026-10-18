package rpc

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/courtapi"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
)

type connFactory func() (*Conn, error)

type handlerRegFunc func(*Conn)

type ReconnectingClient struct {
	factory  connFactory
	local    courtapi.Client
	handlers []handlerRegFunc

	mu       sync.RWMutex
	conn     *Conn
	client   *CourtClient
	restored chan struct{}
	reconCtx context.Context
	cancel   context.CancelFunc

	// reconnectMu serialises reconnect() calls so only one goroutine
	// performs the reconnection; others wait for it to complete.
	reconnectMu sync.Mutex

	eventsMu sync.Mutex
	events   chan any
}

func NewReconnectingClient(factory connFactory, local courtapi.Client) *ReconnectingClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &ReconnectingClient{
		factory:  factory,
		local:    local,
		reconCtx: ctx,
		cancel:   cancel,
		restored: make(chan struct{}),
	}
}

func (rc *ReconnectingClient) RegisterHandler(f handlerRegFunc) {
	rc.mu.Lock()
	rc.handlers = append(rc.handlers, f)
	conn := rc.conn
	rc.mu.Unlock()
	if conn != nil {
		f(conn)
	}
}

func (rc *ReconnectingClient) Start() error {
	conn, err := rc.factory()
	if err != nil {
		return err
	}
	rc.mu.Lock()
	rc.conn = conn
	rc.client = NewCourtClient(conn)
	if rc.restored == nil {
		rc.restored = make(chan struct{})
	}
	rc.mu.Unlock()

	go rc.watchConnection()
	return nil
}

func (rc *ReconnectingClient) Close() {
	rc.cancel()
	rc.mu.RLock()
	if rc.conn != nil {
		rc.conn.Close()
	}
	rc.mu.RUnlock()
}

func (rc *ReconnectingClient) Conn() *Conn {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.conn
}

func (rc *ReconnectingClient) watchConnection() {
	for {
		rc.mu.RLock()
		conn := rc.conn
		rc.mu.RUnlock()

		if conn == nil {
			return
		}

		select {
		case <-rc.reconCtx.Done():
			return
		case <-conn.Done():
			slog.Debug("reconnecting: connection lost, attempting reconnect")
			if err := rc.reconnect(); err != nil {
				slog.Error("reconnecting: failed to reconnect", "err", err)
				return
			}
			slog.Info("reconnecting: reconnected successfully")
		}
	}
}

func (rc *ReconnectingClient) reconnect() error {
	rc.reconnectMu.Lock()
	defer rc.reconnectMu.Unlock()

	// Check if another goroutine already reconnected while we waited
	// for the mutex. If the current conn is alive, there's nothing to do.
	rc.mu.RLock()
	currentConn := rc.conn
	rc.mu.RUnlock()

	// If the connection is not done (still alive), someone else
	// already reconnected — nothing to do.
	if currentConn != nil {
		select {
		case <-currentConn.Done():
			// Connection is dead, proceed with reconnect.
		default:
			return nil
		}
	}

	// Close the dead connection if it hasn't been closed already.
	if currentConn != nil {
		currentConn.Close()
	}

	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for {
		select {
		case <-rc.reconCtx.Done():
			return rc.reconCtx.Err()
		default:
		}

		conn, err := rc.factory()
		if err != nil {
			slog.Debug("reconnecting: factory failed, backing off", "err", err, "backoff", backoff)
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		rc.mu.Lock()
		rc.conn = conn
		rc.client = NewCourtClient(conn)
		if rc.restored != nil {
			close(rc.restored)
		}
		rc.restored = make(chan struct{})
		handlers := rc.handlers
		rc.mu.Unlock()

		for _, h := range handlers {
			h(conn)
		}

		rc.eventsMu.Lock()
		if rc.events != nil {
			SubscribeAll(conn, rc.events)
		}
		rc.eventsMu.Unlock()

		return nil
	}
}

// reconnectIfDead proactively reconnects when the current connection
// is dead. Used by read-only methods that cannot detect connection
// errors from their return values — they get a zero-value instead,
// so we check the connection up front.
func (rc *ReconnectingClient) reconnectIfDead() {
	rc.mu.RLock()
	conn := rc.conn
	rc.mu.RUnlock()
	if conn == nil {
		return
	}
	select {
	case <-conn.Done():
		rc.reconnect()
	default:
	}
}

func (rc *ReconnectingClient) getClient() *CourtClient {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.client
}

// shouldRetry reports whether an error warrants a reconnect-and-retry.
// Only ErrClosed is retried — the call never left the client so it is
// safe for both read-only and mutating methods. ErrPeerDisconnected is
// retried only for read-only methods; mutating methods must not retry
// because the request may have already been applied on the server.
func (rc *ReconnectingClient) shouldRetry(err error) bool {
	return errors.Is(err, ErrClosed)
}

// shouldRetryReadOnly reports whether an error warrants a
// reconnect-and-retry for read-only (idempotent) methods. Both ErrClosed
// and ErrPeerDisconnected are safe because repeating a read has no
// side effects.
func (rc *ReconnectingClient) shouldRetryReadOnly(err error) bool {
	return errors.Is(err, ErrClosed) || errors.Is(err, ErrPeerDisconnected)
}

// reconnectIfError triggers a reconnection when err indicates the
// connection is broken. Returns true when a retry is warranted, false
// otherwise.
func (rc *ReconnectingClient) reconnectIfError(err error, shouldRetry func(error) bool) bool {
	if !shouldRetry(err) {
		return false
	}
	rc.reconnect()
	return true
}

func (rc *ReconnectingClient) GetMinister(id string) court.Minister {
	if rc.local != nil {
		return rc.local.GetMinister(id)
	}
	return nil
}

func (rc *ReconnectingClient) ConfigureModel(client court.LLMProvider, config *court.SessionConfig, repoInfo repo.RepoInfo) {
	if rc.local != nil {
		rc.local.ConfigureModel(client, config, repoInfo)
	}
}

func (rc *ReconnectingClient) CancellableStreamCtx(channelID string) context.Context {
	if rc.local != nil {
		return rc.local.CancellableStreamCtx(channelID)
	}
	return context.Background()
}

func (rc *ReconnectingClient) HasMinister(id string) bool {
	rc.reconnectIfDead()
	client := rc.getClient()
	if client == nil {
		return false
	}
	return client.HasMinister(id)
}

func (rc *ReconnectingClient) ResetMinisterSession(id string, channelID ...string) {
	client := rc.getClient()
	if client != nil {
		client.ResetMinisterSession(id, channelID...)
	}
}

func (rc *ReconnectingClient) EdictKey(edictID uint) storage.EdictKey {
	rc.reconnectIfDead()
	client := rc.getClient()
	if client == nil {
		return storage.EdictKey{}
	}
	return client.EdictKey(edictID)
}

func (rc *ReconnectingClient) CourtEdictKey() storage.EdictKey {
	rc.reconnectIfDead()
	client := rc.getClient()
	if client == nil {
		return storage.EdictKey{}
	}
	return client.CourtEdictKey()
}

// CreateEdict is a mutating method. It does NOT retry on
// ErrPeerDisconnected because the edict may have already been created
// on the server — only on ErrClosed (the call never left the client).
func (rc *ReconnectingClient) CreateEdict(issueRef, intent, sessionID string) (*storage.Edict, error) {
	client := rc.getClient()
	if client == nil {
		return nil, ErrClosed
	}
	edict, err := client.CreateEdict(issueRef, intent, sessionID)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.CreateEdict(issueRef, intent, sessionID)
		}
	}
	return edict, err
}

// CreateEdictSilent is a mutating method. It does NOT retry on
// ErrPeerDisconnected because the edict may have already been created
// on the server — only on ErrClosed (the call never left the client).
func (rc *ReconnectingClient) CreateEdictSilent(issueRef, intent, sessionID string) (*storage.Edict, error) {
	client := rc.getClient()
	if client == nil {
		return nil, ErrClosed
	}
	edict, err := client.CreateEdictSilent(issueRef, intent, sessionID)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.CreateEdictSilent(issueRef, intent, sessionID)
		}
	}
	return edict, err
}

func (rc *ReconnectingClient) GetEdict(edictID uint) (*storage.Edict, error) {
	client := rc.getClient()
	if client == nil {
		return nil, ErrClosed
	}
	edict, err := client.GetEdict(edictID)
	if rc.reconnectIfError(err, rc.shouldRetryReadOnly) {
		if client = rc.getClient(); client != nil {
			return client.GetEdict(edictID)
		}
	}
	return edict, err
}

// GrantRulerSeal is a mutating method. It does NOT retry on
// ErrPeerDisconnected because the seal may have already been granted
// on the server — only on ErrClosed (the call never left the client).
func (rc *ReconnectingClient) GrantRulerSeal(edictID uint, notes string) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.GrantRulerSeal(edictID, notes)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.GrantRulerSeal(edictID, notes)
		}
	}
	return err
}

func (rc *ReconnectingClient) CancelEdict(edictID uint) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.CancelEdict(edictID)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.CancelEdict(edictID)
		}
	}
	return err
}

func (rc *ReconnectingClient) AppendToIntent(edictID uint, clarification string) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.AppendToIntent(edictID, clarification)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.AppendToIntent(edictID, clarification)
		}
	}
	return err
}

// SetIntent is a mutating method. It does NOT retry on
// ErrPeerDisconnected because the intent may have already been set
// on the server — only on ErrClosed (the call never left the client).
func (rc *ReconnectingClient) SetIntent(edictID uint, intent string) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.SetIntent(edictID, intent)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.SetIntent(edictID, intent)
		}
	}
	return err
}

func (rc *ReconnectingClient) ListActiveEdicts() ([]storage.ActiveEdict, error) {
	client := rc.getClient()
	if client == nil {
		return nil, ErrClosed
	}
	edicts, err := client.ListActiveEdicts()
	if rc.reconnectIfError(err, rc.shouldRetryReadOnly) {
		if client = rc.getClient(); client != nil {
			return client.ListActiveEdicts()
		}
	}
	return edicts, err
}

func (rc *ReconnectingClient) CancelZhengming(requestID string) {
	client := rc.getClient()
	if client != nil {
		client.CancelZhengming(requestID)
	}
}

func (rc *ReconnectingClient) HandleZhengmingResponse(ctx context.Context, requestID, answer string) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.HandleZhengmingResponse(ctx, requestID, answer)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.HandleZhengmingResponse(ctx, requestID, answer)
		}
	}
	return err
}

func (rc *ReconnectingClient) AllowRunnerFallback(allow bool) {
	client := rc.getClient()
	if client != nil {
		client.AllowRunnerFallback(allow)
	}
}

func (rc *ReconnectingClient) ClearSessionHistory(tabTarget string) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.ClearSessionHistory(tabTarget)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.ClearSessionHistory(tabTarget)
		}
	}
	return err
}

func (rc *ReconnectingClient) RollbackSession(tabTarget string, snapshot int) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.RollbackSession(tabTarget, snapshot)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.RollbackSession(tabTarget, snapshot)
		}
	}
	return err
}

func (rc *ReconnectingClient) AddSessionMessage(tabTarget, role, content string) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.AddSessionMessage(tabTarget, role, content)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.AddSessionMessage(tabTarget, role, content)
		}
	}
	return err
}

func (rc *ReconnectingClient) AddSessionContextFile(tabTarget, path, content string) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.AddSessionContextFile(tabTarget, path, content)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.AddSessionContextFile(tabTarget, path, content)
		}
	}
	return err
}

func (rc *ReconnectingClient) CompactSession(ctx context.Context, tabTarget, prompt string) (string, error) {
	client := rc.getClient()
	if client == nil {
		return "", ErrClosed
	}
	result, err := client.CompactSession(ctx, tabTarget, prompt)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.CompactSession(ctx, tabTarget, prompt)
		}
	}
	return result, err
}

func (rc *ReconnectingClient) SessionState(tabTarget string) court.SessionState {
	rc.reconnectIfDead()
	client := rc.getClient()
	if client == nil {
		return court.SessionState{}
	}
	return client.SessionState(tabTarget)
}

func (rc *ReconnectingClient) GetEdictSeals(key storage.EdictKey) ([]storage.Seal, error) {
	client := rc.getClient()
	if client == nil {
		return nil, ErrClosed
	}
	seals, err := client.GetEdictSeals(key)
	if rc.reconnectIfError(err, rc.shouldRetryReadOnly) {
		if client = rc.getClient(); client != nil {
			return client.GetEdictSeals(key)
		}
	}
	return seals, err
}

func (rc *ReconnectingClient) PublishEvent(key storage.EdictKey, et storage.CourtEvent, payload storage.JSON) uint {
	client := rc.getClient()
	if client == nil {
		return key.ID
	}
	return client.PublishEvent(key, et, payload)
}

func (rc *ReconnectingClient) RunShellCommand(ctx context.Context, input runners.Input) (runners.Output, error) {
	client := rc.getClient()
	if client == nil {
		return runners.Output{}, ErrClosed
	}
	output, err := client.RunShellCommand(ctx, input)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.RunShellCommand(ctx, input)
		}
	}
	return output, err
}

func (rc *ReconnectingClient) SubmitPrompt(targetID string, p *court.Prompt) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.SubmitPrompt(targetID, p)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.SubmitPrompt(targetID, p)
		}
	}
	return err
}

func (rc *ReconnectingClient) RestoreMinisterSession(tabType string, msgs []schemas.ChatMessage, channelID ...string) error {
	client := rc.getClient()
	if client == nil {
		return ErrClosed
	}
	err := client.RestoreMinisterSession(tabType, msgs, channelID...)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.RestoreMinisterSession(tabType, msgs, channelID...)
		}
	}
	return err
}

func (rc *ReconnectingClient) TakeSnapshot() court.Snapshot {
	client := rc.getClient()
	if client == nil {
		return court.Snapshot{}
	}
	return client.TakeSnapshot()
}

func (rc *ReconnectingClient) CancelTab(channelID string) {
	client := rc.getClient()
	if client != nil {
		client.CancelTab(channelID)
	}
}

func (rc *ReconnectingClient) PauseRitual(channelID string) bool {
	client := rc.getClient()
	if client == nil {
		return false
	}
	return client.PauseRitual(channelID)
}

func (rc *ReconnectingClient) ResumeRitual(channelID string) bool {
	client := rc.getClient()
	if client == nil {
		return false
	}
	return client.ResumeRitual(channelID)
}

func (rc *ReconnectingClient) GetSessionExport(tabTarget string) (*court.SessionExport, error) {
	client := rc.getClient()
	if client == nil {
		return nil, ErrClosed
	}
	export, err := client.GetSessionExport(tabTarget)
	if rc.reconnectIfError(err, rc.shouldRetryReadOnly) {
		if client = rc.getClient(); client != nil {
			return client.GetSessionExport(tabTarget)
		}
	}
	return export, err
}

func (rc *ReconnectingClient) Subscribe(ctx context.Context) <-chan any {
	rc.eventsMu.Lock()
	defer rc.eventsMu.Unlock()
	if rc.events == nil {
		rc.events = make(chan any, 256)
		rc.mu.RLock()
		conn := rc.conn
		rc.mu.RUnlock()
		if conn != nil {
			SubscribeAll(conn, rc.events)
		}
	}
	return rc.events
}

// SetContext updates the court's session configuration (model, API keys,
// repo info) over the wire. The daemon re-initializes Bifrost and calls
// ConfigureModel internally after every SetContext.
func (rc *ReconnectingClient) SetContext(ctx context.Context, params types.SetContextParams) error {
	rc.mu.RLock()
	client := rc.client
	rc.mu.RUnlock()
	if client == nil {
		return ErrClosed
	}
	err := client.SetContext(ctx, params)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.SetContext(ctx, params)
		}
	}
	return err
}

// EnsureEdictWorktree creates edictID's worktree on the court's side of the
// wire and returns its project-root-relative path, retrying on a broken
// connection like other methods.
func (rc *ReconnectingClient) EnsureEdictWorktree(ctx context.Context, edictID uint) (string, error) {
	client := rc.getClient()
	if client == nil {
		return "", ErrClosed
	}
	rel, err := client.EnsureEdictWorktree(ctx, edictID)
	if rc.reconnectIfError(err, rc.shouldRetry) {
		if client = rc.getClient(); client != nil {
			return client.EnsureEdictWorktree(ctx, edictID)
		}
	}
	return rel, err
}

// ConnDone returns a channel that is closed when the current underlying
// connection drops. Callers should re-call ConnDone after a reconnect
// to get the new connection's channel. Returns a never-closed channel
// if there is no current connection.
func (rc *ReconnectingClient) ConnDone() <-chan struct{} {
	rc.mu.RLock()
	conn := rc.conn
	rc.mu.RUnlock()
	if conn == nil {
		return make(chan struct{})
	}
	return conn.Done()
}

// Restored returns a channel that is closed once the client has swapped in a
// fresh connection after a drop. Callers should re-read it after each drop to
// wait for the next reconnect, mirroring the ConnDone contract.
func (rc *ReconnectingClient) Restored() <-chan struct{} {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.restored
}

// ListModels reads models over the wire from the court that owns the
// LLM client, retrying on a broken connection like other read-only
// methods.
func (rc *ReconnectingClient) ListModels(provider string) (*schemas.BifrostListModelsResponse, error) {
	client := rc.getClient()
	if client == nil {
		return nil, ErrClosed
	}
	models, err := client.ListModels(provider)
	if rc.reconnectIfError(err, rc.shouldRetryReadOnly) {
		if client = rc.getClient(); client != nil {
			return client.ListModels(provider)
		}
	}
	return models, err
}

// ApproximateContextSize reads the registry over the wire, retrying on a
// broken connection like other read-only methods.
func (rc *ReconnectingClient) ApproximateContextSize(provider, model string) int {
	rc.reconnectIfDead()
	client := rc.getClient()
	if client == nil {
		return 0
	}
	size := client.ApproximateContextSize(provider, model)
	return size
}

var _ courtapi.Client = (*ReconnectingClient)(nil)
