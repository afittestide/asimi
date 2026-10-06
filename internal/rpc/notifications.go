package rpc

import (
	"context"
	"log/slog"
	"reflect"
	"sync"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/wire"
)

// Notification method names (server → client). Keep in sync with the
// typeToMethod registry below.
const (
	NotifyStreamStart                = "stream.start"
	NotifyStreamChunk                = "stream.chunk"
	NotifyStreamComplete             = "stream.complete"
	NotifyStreamDone                 = "stream.done"
	NotifyStreamInterrupted          = "stream.interrupted"
	NotifyStreamMaxTokens            = "stream.max_tokens"
	NotifyStreamContextOverflow      = "stream.context_overflow"
	NotifyStreamError                = "stream.error"
	NotifyEventsDrained              = "events.drained"
	NotifyMinisterInvoking           = "minister.invoking"
	NotifyMinisterCompleted          = "minister.completed"
	NotifyEvent                      = "event"
	NotifyZhengmingPending           = "zhengming.pending"
	NotifyZhengmingAnswered          = "zhengming.answered"
	NotifyRitualStep                 = "ritual.step"
	NotifyContainerLaunched          = "runner.container_launched"
	NotifyToolCallScheduled          = "toolcall.scheduled"
	NotifyToolCallExecuting          = "toolcall.executing"
	NotifyToolCallWaitingForApproval = "toolcall.waiting_for_approval"
	NotifyToolCallSuccess            = "toolcall.success"
	NotifyToolCallError              = "toolcall.error"
	NotifyToolCallAborted            = "toolcall.aborted"
)

// typeToMethod maps a Go notification type to its wire method name.
// Used by the server-side dispatcher to route messages coming out of
// court.Subscribe. Extendable: call RegisterNotificationType.
var (
	typeToMethodMu sync.RWMutex
	typeToMethod   = map[reflect.Type]string{
		reflect.TypeOf(court.StreamStartMsg{}):                  NotifyStreamStart,
		reflect.TypeOf(court.StreamChunkMsg{}):                  NotifyStreamChunk,
		reflect.TypeOf(court.StreamCompleteMsg{}):               NotifyStreamComplete,
		reflect.TypeOf(court.StreamDoneMsg{}):                   NotifyStreamDone,
		reflect.TypeOf(court.StreamInterruptedMsg{}):            NotifyStreamInterrupted,
		reflect.TypeOf(court.StreamMaxTokensReachedMsg{}):       NotifyStreamMaxTokens,
		reflect.TypeOf(court.StreamContextOverflowMsg{}):        NotifyStreamContextOverflow,
		reflect.TypeOf(court.StreamErrorMsg{}):                  NotifyStreamError,
		reflect.TypeOf(court.EventsDrainedMsg{}):                NotifyEventsDrained,
		reflect.TypeOf(court.MinisterInvokingMsg{}):             NotifyMinisterInvoking,
		reflect.TypeOf(court.MinisterCompletedMsg{}):            NotifyMinisterCompleted,
		reflect.TypeOf(court.EventNotificationMsg{}):            NotifyEvent,
		reflect.TypeOf(court.ZhengmingPendingMsg{}):             NotifyZhengmingPending,
		reflect.TypeOf(court.ZhengmingAnsweredMsg{}):            NotifyZhengmingAnswered,
		reflect.TypeOf(court.RitualStepMsg{}):                   NotifyRitualStep,
		reflect.TypeOf(runners.ContainerLaunchedMsg{}):          NotifyContainerLaunched,
		reflect.TypeOf(runners.ToolCallScheduledMsg{}):          NotifyToolCallScheduled,
		reflect.TypeOf(runners.ToolCallExecutingMsg{}):          NotifyToolCallExecuting,
		reflect.TypeOf(runners.ToolCallWaitingForApprovalMsg{}): NotifyToolCallWaitingForApproval,
		reflect.TypeOf(runners.ToolCallSuccessMsg{}):            NotifyToolCallSuccess,
		reflect.TypeOf(runners.ToolCallErrorMsg{}):              NotifyToolCallError,
		reflect.TypeOf(runners.ToolCallAbortedMsg{}):            NotifyToolCallAborted,
	}
)

// RegisterNotificationType associates a Go type with a wire method name.
// Idempotent; later registrations win.
func RegisterNotificationType(msg any, method string) {
	typeToMethodMu.Lock()
	defer typeToMethodMu.Unlock()
	typeToMethod[reflect.TypeOf(msg)] = method
}

// methodForType looks up the wire method for a runtime value.
func methodForType(v any) (string, bool) {
	typeToMethodMu.RLock()
	defer typeToMethodMu.RUnlock()
	m, ok := typeToMethod[reflect.TypeOf(v)]
	return m, ok
}

// PumpNotifications subscribes to the given Client and forwards every
// message to conn as a wire notification keyed by method name. Unknown
// message types are dropped with a debug log. Runs until ctx is done.
func PumpNotifications(ctx context.Context, conn *Conn, events <-chan any) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-events:
			if !ok {
				return
			}
			method, ok := methodForType(msg)
			if !ok {
				slog.Debug("rpc: no wire method for notification", "type", reflect.TypeOf(msg))
				continue
			}
			if err := conn.Notify(method, msg); err != nil {
				slog.Debug("rpc: notify failed", "method", method, "err", err)
			}
		}
	}
}

// NotificationDecoder decodes raw notification params into a concrete
// Go value. The client-side registry below wires one per known method.
type NotificationDecoder func([]byte) (any, error)

var (
	methodToDecoderMu sync.RWMutex
	methodToDecoder   = map[string]NotificationDecoder{
		NotifyStreamStart:                decode[court.StreamStartMsg],
		NotifyStreamChunk:                decode[court.StreamChunkMsg],
		NotifyStreamComplete:             decode[court.StreamCompleteMsg],
		NotifyStreamDone:                 decode[court.StreamDoneMsg],
		NotifyStreamInterrupted:          decode[court.StreamInterruptedMsg],
		NotifyStreamMaxTokens:            decode[court.StreamMaxTokensReachedMsg],
		NotifyStreamContextOverflow:      decode[court.StreamContextOverflowMsg],
		NotifyStreamError:                decode[court.StreamErrorMsg],
		NotifyEventsDrained:              decode[court.EventsDrainedMsg],
		NotifyMinisterInvoking:           decode[court.MinisterInvokingMsg],
		NotifyMinisterCompleted:          decode[court.MinisterCompletedMsg],
		NotifyEvent:                      decode[court.EventNotificationMsg],
		NotifyZhengmingPending:           decode[court.ZhengmingPendingMsg],
		NotifyZhengmingAnswered:          decode[court.ZhengmingAnsweredMsg],
		NotifyRitualStep:                 decode[court.RitualStepMsg],
		NotifyContainerLaunched:          decode[runners.ContainerLaunchedMsg],
		NotifyToolCallScheduled:          decode[runners.ToolCallScheduledMsg],
		NotifyToolCallExecuting:          decode[runners.ToolCallExecutingMsg],
		NotifyToolCallWaitingForApproval: decode[runners.ToolCallWaitingForApprovalMsg],
		NotifyToolCallSuccess:            decode[runners.ToolCallSuccessMsg],
		NotifyToolCallError:              decode[runners.ToolCallErrorMsg],
		NotifyToolCallAborted:            decode[runners.ToolCallAbortedMsg],
	}
)

// RegisterNotificationDecoder associates a wire method with a decoder.
// Mirrors RegisterNotificationType for client-side extensibility.
func RegisterNotificationDecoder(method string, d NotificationDecoder) {
	methodToDecoderMu.Lock()
	defer methodToDecoderMu.Unlock()
	methodToDecoder[method] = d
}

// decode is a generic helper that builds a NotificationDecoder for type T.
func decode[T any](params []byte) (any, error) {
	var v T
	if err := wire.Decode(params, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// SubscribeAll registers a NotifyHandler on conn for every known
// notification method. Decoded values flow into out. The returned
// function unregisters handlers (best-effort: the Conn's handler map is
// not currently concurrent-safe for deletion, so unregister is a no-op
// today — included for API symmetry).
//
// Callers drain out until ctx is done.
func SubscribeAll(conn *Conn, out chan<- any) {
	methodToDecoderMu.RLock()
	defer methodToDecoderMu.RUnlock()
	for method, dec := range methodToDecoder {
		method, dec := method, dec // capture
		conn.HandleNotify(method, func(ctx context.Context, params []byte) {
			v, err := dec(params)
			if err != nil {
				slog.Debug("rpc: decode notification", "method", method, "err", err)
				return
			}
			select {
			case out <- v:
			case <-ctx.Done():
			}
		})
	}
}
