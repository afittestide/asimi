package rpc

import (
	"context"
	"errors"
	"log/slog"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/wire"
)

// MethodRunEditor is the daemon→TUI method that opens content in $EDITOR.
const MethodRunEditor = "tui.run_editor"

// RunEditorParams is the wire payload for an editor request.
type RunEditorParams struct {
	Content  string `msgpack:"content"`
	Filename string `msgpack:"filename"`
}

// RunEditorResult carries the post-edit content back to the daemon. Err is
// transported as a string because errors aren't serializable. Outcome crosses
// the wire explicitly so a daemon-hosted TUI cannot reintroduce the ambiguity
// an implicit "saved" boolean had (see tools.EditorOutcome).
type RunEditorResult struct {
	Content string `msgpack:"content"`
	Outcome string `msgpack:"outcome"`
	Err     string `msgpack:"err,omitempty"`
}

// RegisterEditorHandler wires a handler on the TUI's *Conn that converts an
// inbound Call(MethodRunEditor) into a local EditorRequest the TUI already
// knows how to render (via the tools.EditorRequest case in Update), then
// blocks until the TUI returns the modified content.
func RegisterEditorHandler(conn *Conn, program ProgramSender) {
	conn.Handle(MethodRunEditor, func(ctx context.Context, params []byte) ([]byte, error) {
		var p RunEditorParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		respCh := make(chan tools.EditorResult, 1)
		program.Send(tools.EditorRequest{
			Content:    p.Content,
			Filename:   p.Filename,
			ResultChan: respCh,
		})
		select {
		case res := <-respCh:
			out := RunEditorResult{Content: res.Content, Outcome: string(res.Outcome)}
			if res.Err != nil {
				out.Err = res.Err.Error()
			}
			return wire.Encode(out)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
}

// RequestEditor is the daemon-side call: issues the RPC and returns the
// modified content (or an error if the editor failed).
func RequestEditor(ctx context.Context, conn *Conn, content, filename string) (tools.EditorResult, error) {
	raw, err := conn.Call(ctx, MethodRunEditor, RunEditorParams{Content: content, Filename: filename})
	if err != nil {
		return tools.EditorResult{}, err
	}
	var r RunEditorResult
	if err := wire.Decode(raw, &r); err != nil {
		return tools.EditorResult{}, err
	}
	res := tools.EditorResult{Content: r.Content, Outcome: tools.EditorOutcome(r.Outcome)}
	if r.Err != "" {
		res.Err = errors.New(r.Err)
	}
	return res, nil
}

// interceptEditor bridges a daemon-side EditorRequest to an RPC call to the
// TUI. Returns true if the message was handled.
func interceptEditor(ctx context.Context, conn *Conn, msg any) bool {
	req, ok := msg.(tools.EditorRequest)
	if !ok {
		return false
	}
	go func() {
		res, err := RequestEditor(ctx, conn, req.Content, req.Filename)
		if err != nil {
			res = tools.EditorResult{Outcome: tools.EditorFailed, Err: err}
		}
		if req.ResultChan != nil {
			select {
			case req.ResultChan <- res:
			default:
				slog.Warn("rpc: editor result channel full or closed")
			}
		}
	}()
	return true
}
