// tool_dispatch.go implements the Court-side of the tool-facing dispatch
// interfaces (tools.ZhengmingRequester, tools.MinisterConsultant,
// tools.RitualLauncher, HostChecker). These are the callbacks that tools
// (ask_ruler, consult_minister, enact_ritual, run_shell_command)
// invoke to access Court services — requesting clarifications, dispatching
// work to other ministers, starting rituals, and checking host command safety.
package court

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/storage"
)

// zhengmingDispatch is the Court-owned state for zhengming request/wait
// coordination. It replaces the per-MinisterBase pendingZhengming map that
// was previously accessed through the chancellor's MinisterBase.
type zhengmingDispatch struct {
	mu      sync.Mutex
	pending map[string]chan ZhengmingAnswer
}

func newZhengmingDispatch() *zhengmingDispatch {
	return &zhengmingDispatch{
		pending: make(map[string]chan ZhengmingAnswer),
	}
}

// zhengmingRaisedFirer is implemented by *MinisterBase (via promotion on
// *ministerImpl). The Court uses this to fire the onZhengmingRaised callback
// on the minister that called RequestZhengming.
type zhengmingRaisedFirer interface {
	fireZhengmingRaised()
}

// --- ZhengmingRequester (tools.ZhengmingRequester) ---

// RequestZhengming creates a clarification request in the DB, notifies the
// UI, fires the onZhengmingRaised callback on the calling minister (if any),
// and emits a zhengming_requested event.
func (s *Court) RequestZhengming(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
	requestID := GenerateID("zhengming", fmt.Sprintf("%d", key.ID), callerMinisterID, fmt.Sprintf("%v", questions), time.Now().String())

	// Prefer session ID from context (the session that's actually executing
	// the tool). Fall back to the minister's interactive session for backward
	// compatibility when no context value is present.
	sessionID := tools.SessionIDFromContext(ctx)
	if sessionID == "" {
		if caller := s.GetMinister(callerMinisterID); caller != nil {
			if sess := caller.GetSession(); sess != nil {
				sessionID = sess.ID
			}
		}
	}

	// The deadline is written but deliberately not consumed (Ruler ruling
	// 2026-11-18: a question never goes stale — it is answered by the Ruler or
	// ends when its edict seals). Do not add a timeout_at reader without a new
	// ruling. See also: the terminal-dispositions vocabulary in
	// storage/court_schema.go.
	req := storage.Zhengming{
		RequestID:  requestID,
		EdictID:    key.ID,
		Username:   key.Username,
		Project:    key.Project,
		MinisterID: callerMinisterID,
		SessionID:  sessionID,
		Questions:  questions,
		Priority:   priority,
		Status:     storage.ZhengmingPending,
		TimeoutAt:  time.Now().Add(24 * time.Hour),
	}

	if priority == storage.PriorityUrgent {
		req.TimeoutAt = time.Now().Add(1 * time.Hour)
	}

	if err := s.db.Create(&req).Error; err != nil {
		return "", fmt.Errorf("failed to create zhengming request: %w", err)
	}

	// Fire the onZhengmingRaised callback on the calling minister so the
	// ritual runner's step timer pauses. This fixes the pre-existing issue
	// where the callback was fired on the chancellor instead of the caller.
	if firer, ok := s.GetMinister(callerMinisterID).(zhengmingRaisedFirer); ok {
		firer.fireZhengmingRaised()
	}

	if s.notify != nil {
		s.notify(ZhengmingPendingMsg{
			RequestID:  requestID,
			EdictKey:   key,
			MinisterID: callerMinisterID,
			ChannelID:  tools.ChannelIDFromContext(ctx),
			Questions:  questions,
			Priority:   priority,
		})
	}

	s.PublishEvent(key, "zhengming_requested", storage.JSON{
		"request_id":  requestID,
		"minister_id": callerMinisterID,
		"questions":   questions,
		"priority":    string(priority),
	})

	return requestID, nil
}

// WaitForZhengming blocks until the zhengming answer arrives or ctx is cancelled.
func (s *Court) WaitForZhengming(ctx context.Context, requestID string) (string, error) {
	ch := make(chan ZhengmingAnswer, 1)
	s.zhengming.mu.Lock()
	s.zhengming.pending[requestID] = ch
	s.zhengming.mu.Unlock()

	defer func() {
		s.zhengming.mu.Lock()
		delete(s.zhengming.pending, requestID)
		s.zhengming.mu.Unlock()
	}()

	select {
	case answer := <-ch:
		return answer.Answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// DeliverZhengmingAnswer delivers a zhengming answer to a waiting caller.
// Returns true if the answer was delivered.
func (s *Court) DeliverZhengmingAnswer(answer ZhengmingAnswer) bool {
	s.zhengming.mu.Lock()
	ch, ok := s.zhengming.pending[answer.RequestID]
	s.zhengming.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- answer:
		return true
	default:
		return false
	}
}

// CancelZhengmingDispatch cancels a pending zhengming wait, unblocking
// WaitForZhengming by deleting the channel from the pending map.
func (s *Court) CancelZhengmingDispatch(requestID string) {
	if s == nil || s.zhengming == nil {
		return
	}
	s.zhengming.mu.Lock()
	defer s.zhengming.mu.Unlock()
	delete(s.zhengming.pending, requestID)
}

// --- MinisterConsultant (tools.MinisterConsultant) ---

// ConsultMinister dispatches work to a registered minister synchronously.
// callerMinisterID is the minister ID of the caller (for session lookup and
// self-consult guard). channelID is the routing target for streaming output.
func (s *Court) ConsultMinister(ctx context.Context, callerMinisterID, ministerID, channelID string, key storage.EdictKey, work string) (string, error) {
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}

	if callerMinisterID == ministerID {
		return "", fmt.Errorf("a minister cannot consult itself (caller: %s, target: %s) — use direct execution instead", callerMinisterID, ministerID)
	}

	if s.notify != nil {
		s.notify(MinisterInvokingMsg{
			ChannelID:  channelID,
			MinisterID: ministerID,
			EdictKey:   key,
			Task:       work,
		})
	}

	minister := s.GetMinister(ministerID)
	if minister == nil {
		err := fmt.Errorf("minister not found: %s", ministerID)
		if s.notify != nil {
			s.notify(MinisterCompletedMsg{
				ChannelID:  channelID,
				MinisterID: ministerID,
				EdictKey:   key,
				Error:      err,
			})
		}
		return "", fmt.Errorf("minister %s failed: %w", ministerID, err)
	}

	// Wrap notify so the invoked minister's session routes to the caller's tab.
	// Use callerMinisterID for the session lookup, channelID for routing.
	var wrappedNotify internal.NotifyFunc
	if s.notify != nil {
		var sess *Session
		if caller := s.GetMinister(callerMinisterID); caller != nil {
			sess = caller.GetSession()
		}
		wrappedNotify = WithChannelID(s.notify, sess, channelID)
	}

	doneChan := make(chan Result, 1)
	task := &Task{
		Ctx:          ctx,
		EdictKey:     key,
		Work:         work,
		Done:         doneChan,
		Notify:       wrappedNotify,
		ChannelID:    channelID,
		ExcludeTools: []string{"consult_minister"},
	}

	select {
	case minister.Tasks() <- task:
		logger.Info("task sent to minister",
			"minister", ministerID,
			"edict_id", key.ID,
			"work", utils.TruncateMiddle(work, 50))
	case <-ctx.Done():
		return "", fmt.Errorf("minister %s failed: context cancelled while sending task to %s", ministerID, ministerID)
	}

	var result Result
	select {
	case result = <-doneChan:
	case <-ctx.Done():
		return "", fmt.Errorf("minister %s failed: %w", ministerID, ctx.Err())
	}

	if result.Err != nil {
		if s.notify != nil {
			s.notify(MinisterCompletedMsg{
				ChannelID:  channelID,
				MinisterID: ministerID,
				EdictKey:   key,
				Error:      result.Err,
			})
		}
		logger.Error("task returned error",
			"minister", ministerID,
			"edict_id", key.ID,
			"error", result.Err)
		return "", fmt.Errorf("minister %s failed: %w", ministerID, result.Err)
	}

	if s.notify != nil {
		s.notify(MinisterCompletedMsg{
			ChannelID:  channelID,
			MinisterID: ministerID,
			EdictKey:   key,
			Output:     work,
			Sealed:     true,
		})
	}

	logger.Info("task completed",
		"minister", ministerID,
		"edict_id", key.ID,
		"sealed", result.Sealed,
		"output_len", len(result.Output))

	resultMap := map[string]any{
		"minister_id": ministerID,
		"edict_id":    key.ID,
		"status":      "completed",
		"sealed":      result.Sealed,
		"output":      result.Output,
	}
	resultJSON, _ := json.Marshal(resultMap)
	return string(resultJSON), nil
}

// --- RitualLauncher (tools.RitualLauncher) ---

// StartRitual emits a ritual_enacted event for the RitualGuard to handle
// asynchronously.
func (s *Court) StartRitual(name string, key storage.EdictKey, inputs map[string]string) error {
	reg := s.GetRitualRegistry()
	if reg == nil {
		return fmt.Errorf("unknown ritual %q; no rituals loaded", name)
	}
	if reg.Get(name) == nil {
		return fmt.Errorf("unknown ritual %q; available rituals: %s", name, strings.Join(reg.List(), ", "))
	}

	if inputs == nil {
		inputs = make(map[string]string)
	}
	inputs["edict_id"] = fmt.Sprintf("%d", key.ID)

	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}

	inputsPayload := make(map[string]interface{}, len(inputs))
	for k, v := range inputs {
		inputsPayload[k] = v
	}
	payload := storage.JSON{
		"ritual_name": name,
		"inputs":      inputsPayload,
	}

	// Advisory check: a running execution of this edict with the same ritual
	// means the work is already in flight, so re-enacting it would restart the
	// ritual from step 0. Refuse loudly instead. This is NOT the concurrency
	// guard — the per-edict lock remains the serialization point, and racing
	// enactments that both pass this check resolve there (the second queues).
	// A Court built without a DB (direct construction, tests) has nothing to
	// check against and publishes as before.
	if s.db != nil {
		var running []RitualExecution
		if err := s.db.Where("edict_id = ? AND username = ? AND project = ? AND state = ?",
			key.ID, key.Username, key.Project, RitualStateRunning).Find(&running).Error; err != nil {
			logger.Warn("failed to check for in-flight ritual", "ritual", name, "edict_id", key.ID, "error", err)
		}
		for _, exec := range running {
			if exec.RitualName == name {
				return fmt.Errorf("edict %d already has %s running (execution %s); the same ritual is not re-enacted",
					key.ID, name, exec.ID)
			}
		}
	}

	s.PublishEvent(key, storage.EventRitualEnacted, payload)

	logger.Info("ritual requested", "ritual", name, "edict_id", key.ID)
	return nil
}

// --- HostChecker ---

// HostExecRefusedError is returned when a command is routed to the host but the
// session's mandate carries no host-execution grant. It is a REFUSAL, not an
// outage, and callers must surface it rather than retrying the command
// somewhere with more authority — running on the host is strictly more
// authority than the sandbox, so a fallback would grant what the mandate did
// not (edict 1036, seam 7).
type HostExecRefusedError struct {
	Command string
	// Reason says why no grant was found, so the refusal is actionable.
	Reason string
}

func (e HostExecRefusedError) Error() string {
	return fmt.Sprintf("refusing to run %q on the host: %s; "+
		"host execution needs an affirmative grant and none was given", e.Command, e.Reason)
}

// CheckHostCommand reports whether a command must run on the host
// (runOnHost), whether that needs a per-command approval (needsApproval), and
// any refusal (err).
//
// The mandate is the sole authority: run_on_host/safe_run_on_host in the
// committed config are only a *request* to reach the host. Host execution is
// strictly more authority than the sandbox, so the pattern decides what would
// run on the host, while the Ruler's SeamHostExec grant decides whether it may.
// Without the grant — which is the no-dialog case, where the mandate is
// RestrictiveMandate — the command is refused, fail-closed, with a typed error.
//
// isolated-host is checked first and is not a mandate question at all: the
// operator bypassed the sandbox on the command line, so there is no host seam to
// gate (seam 8, deliberately not gated).
func (s *Court) CheckHostCommand(cmd string) (runOnHost, needsApproval bool, err error) {
	// In isolated-host mode, all commands run on host without approval. This is
	// the operator's explicit CLI bypass (seam 8), outside the mandate's reach.
	if s.isolatedHost {
		return true, false, nil
	}

	sandbox := s.hostPatterns()
	if sandbox.RunOnHost == nil && sandbox.SafeRunOnHost == nil {
		return false, false, nil
	}

	// Check SafeRunOnHost first (higher priority - no approval needed)
	matched := false
	for _, pattern := range sandbox.SafeRunOnHost {
		if ok, _ := regexp.MatchString(pattern, cmd); ok {
			matched, needsApproval = true, false
			break
		}
	}

	// Check RunOnHost patterns (requires approval)
	if !matched {
		for _, pattern := range sandbox.RunOnHost {
			if ok, _ := regexp.MatchString(pattern, cmd); ok {
				matched, needsApproval = true, true
				break
			}
		}
	}

	if !matched {
		return false, false, nil
	}

	if reason, granted := s.hostExecGrant(); !granted {
		return false, false, HostExecRefusedError{Command: cmd, Reason: reason}
	}
	return true, needsApproval, nil
}

// hostPatterns returns the sandbox config whose run_on_host patterns decide
// what *would* run on the host: the mandate's granted config when a mandate is
// bound, else the session config. The patterns are not narrowed by the mandate
// (they only say which commands belong on the host), but reading the granted
// config keeps this path on the same authority as the container it describes.
func (s *Court) hostPatterns() config.SandboxConfig {
	if m := s.Mandate(); m != nil && m.Config != nil {
		return m.Config.Sandbox
	}
	if s.sessionCfg != nil {
		return s.sessionCfg.Sandbox
	}
	return config.SandboxConfig{}
}

// hostExecGrant reports whether the session's mandate carries the explicit
// host-execution grant, and a human-readable reason when it does not. It reads
// the grant record, never the effective config: run_on_host in the config is a
// request, and only Mandate.Grants pins the Ruler's decision.
func (s *Court) hostExecGrant() (reason string, granted bool) {
	s.mandateMu.RLock()
	m := s.mandate
	s.mandateMu.RUnlock()

	if m == nil {
		return "no mandate is bound, so no host-execution grant exists", false
	}
	g, ok := m.GrantFor(config.SeamHostExec, config.SeamHostRunner)
	if !ok {
		return "the mandate records no host-execution decision", false
	}
	if !g.Granted {
		return "the Ruler declined host execution for this session", false
	}
	return "", true
}
