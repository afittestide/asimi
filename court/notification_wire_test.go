package court

import (
	"errors"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func TestStreamChunkMsgRoundTrip(t *testing.T) {
	in := StreamChunkMsg{ChannelID: "ruling", Text: "hello world"}
	b, err := msgpack.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out StreamChunkMsg
	if err := msgpack.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round trip: got %+v want %+v", out, in)
	}
}

func TestStreamStartMsgRoundTrip(t *testing.T) {
	in := StreamStartMsg{ChannelID: "ruling", EdictID: 42}
	b, _ := msgpack.Marshal(in)
	var out StreamStartMsg
	_ = msgpack.Unmarshal(b, &out)
	if out != in {
		t.Fatalf("%+v != %+v", out, in)
	}
}

func TestStreamCompleteMsgRoundTrip(t *testing.T) {
	in := StreamCompleteMsg{ChannelID: "forge"}
	b, _ := msgpack.Marshal(in)
	var out StreamCompleteMsg
	_ = msgpack.Unmarshal(b, &out)
	if out != in {
		t.Fatalf("%+v != %+v", out, in)
	}
}

func TestStreamInterruptedMsgRoundTrip(t *testing.T) {
	in := StreamInterruptedMsg{ChannelID: "ruling", PartialContent: "partial"}
	b, _ := msgpack.Marshal(in)
	var out StreamInterruptedMsg
	_ = msgpack.Unmarshal(b, &out)
	if out != in {
		t.Fatalf("%+v != %+v", out, in)
	}
}

func TestStreamMaxTokensReachedMsgRoundTrip(t *testing.T) {
	in := StreamMaxTokensReachedMsg{ChannelID: "judge", Content: "the end"}
	b, _ := msgpack.Marshal(in)
	var out StreamMaxTokensReachedMsg
	_ = msgpack.Unmarshal(b, &out)
	if out != in {
		t.Fatalf("%+v != %+v", out, in)
	}
}

func TestStreamContextOverflowMsgRoundTrip(t *testing.T) {
	in := StreamContextOverflowMsg{ChannelID: "ruling", Window: 131072}
	b, err := msgpack.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out StreamContextOverflowMsg
	if err := msgpack.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round trip: got %+v want %+v", out, in)
	}
}

func TestStreamContextOverflowMsgRoundTripUnknownWindow(t *testing.T) {
	in := StreamContextOverflowMsg{ChannelID: "forge"}
	b, _ := msgpack.Marshal(in)
	var out StreamContextOverflowMsg
	_ = msgpack.Unmarshal(b, &out)
	if out != in {
		t.Fatalf("round trip: got %+v want %+v", out, in)
	}
}

func TestStreamErrorMsgRoundTrip(t *testing.T) {
	in := StreamErrorMsg{ChannelID: "ruling", Err: errors.New("something broke")}
	b, err := msgpack.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out StreamErrorMsg
	if err := msgpack.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ChannelID != in.ChannelID {
		t.Fatalf("channel: %q want %q", out.ChannelID, in.ChannelID)
	}
	if out.Err == nil || out.Err.Error() != in.Err.Error() {
		t.Fatalf("err: %v want %v", out.Err, in.Err)
	}
}

func TestStreamErrorMsgRoundTripNilErr(t *testing.T) {
	in := StreamErrorMsg{ChannelID: "forge"}
	b, _ := msgpack.Marshal(in)
	var out StreamErrorMsg
	_ = msgpack.Unmarshal(b, &out)
	if out.ChannelID != "forge" || out.Err != nil {
		t.Fatalf("got %+v", out)
	}
}

func TestStreamErrorMsgRoundTripWithPartialContent(t *testing.T) {
	in := StreamErrorMsg{
		ChannelID:      "ruling",
		Err:            errors.New("upstream failed"),
		PartialContent: "partial text before error",
	}
	b, err := msgpack.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out StreamErrorMsg
	if err := msgpack.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ChannelID != in.ChannelID {
		t.Fatalf("channel: %q want %q", out.ChannelID, in.ChannelID)
	}
	if out.Err == nil || out.Err.Error() != in.Err.Error() {
		t.Fatalf("err: %v want %v", out.Err, in.Err)
	}
	if out.PartialContent != in.PartialContent {
		t.Fatalf("partial_content: %q want %q", out.PartialContent, in.PartialContent)
	}
}
