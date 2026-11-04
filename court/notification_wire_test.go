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

// TestStreamChunkMsgRoundTripAttribution locks the ling attribution rider into
// the msgpack wire contract: both tokens must survive encoding unchanged and
// stay absent (empty) for unattributed chunks.
func TestStreamChunkMsgRoundTripAttribution(t *testing.T) {
	in := StreamChunkMsg{
		ChannelID:  "e994",
		Text:       "attributed",
		Reasoning:  "thinking",
		LingID:     "ling-123",
		MinisterID: "forge",
	}
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

	// Unattributed chunks keep the tokens empty over the wire.
	b, err = msgpack.Marshal(StreamChunkMsg{ChannelID: "forge", Text: "plain"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var plain StreamChunkMsg
	if err := msgpack.Unmarshal(b, &plain); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if plain.LingID != "" || plain.MinisterID != "" {
		t.Fatalf("unattributed chunk leaked attribution: %+v", plain)
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
	in := StreamCompleteMsg{ChannelID: "forge", LingID: "ling-1"}
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

// TestTerminalMsgLingAttributionRoundTrip locks the same rider onto the two
// terminal messages: an error or interrupt of one ling must be attributable
// after crossing the wire, and stay empty for channel-wide messages.
func TestTerminalMsgLingAttributionRoundTrip(t *testing.T) {
	inErr := StreamErrorMsg{ChannelID: "e994", Err: errors.New("boom"), PartialContent: "half", LingID: "ling-1"}
	b, err := msgpack.Marshal(inErr)
	if err != nil {
		t.Fatalf("marshal error msg: %v", err)
	}
	var outErr StreamErrorMsg
	if err := msgpack.Unmarshal(b, &outErr); err != nil {
		t.Fatalf("unmarshal error msg: %v", err)
	}
	if outErr.LingID != "ling-1" || outErr.ChannelID != "e994" || outErr.PartialContent != "half" {
		t.Fatalf("error msg round trip: %+v", outErr)
	}

	inInt := StreamInterruptedMsg{ChannelID: "e994", PartialContent: "half", LingID: "ling-2"}
	b, err = msgpack.Marshal(inInt)
	if err != nil {
		t.Fatalf("marshal interrupted msg: %v", err)
	}
	var outInt StreamInterruptedMsg
	if err := msgpack.Unmarshal(b, &outInt); err != nil {
		t.Fatalf("unmarshal interrupted msg: %v", err)
	}
	if outInt != inInt {
		t.Fatalf("interrupted msg round trip: got %+v want %+v", outInt, inInt)
	}

	// Channel-wide messages keep an empty token.
	var plainInt StreamInterruptedMsg
	b, _ = msgpack.Marshal(StreamInterruptedMsg{ChannelID: "e994"})
	if err := msgpack.Unmarshal(b, &plainInt); err != nil {
		t.Fatalf("unmarshal plain interrupted msg: %v", err)
	}
	if plainInt.LingID != "" {
		t.Fatalf("unattributed interrupt leaked attribution: %+v", plainInt)
	}
}

// TestRemainingTerminalMsgLingAttributionRoundTrip extends the rider contract to
// the truncation and overflow messages: both are per-ling terminal events, so
// their attribution must survive the wire and stay empty for channel-wide ones.
func TestRemainingTerminalMsgLingAttributionRoundTrip(t *testing.T) {
	inMax := StreamMaxTokensReachedMsg{ChannelID: "e994", Content: "cut", LingID: "ling-1"}
	b, err := msgpack.Marshal(inMax)
	if err != nil {
		t.Fatalf("marshal max tokens: %v", err)
	}
	var outMax StreamMaxTokensReachedMsg
	if err := msgpack.Unmarshal(b, &outMax); err != nil {
		t.Fatalf("unmarshal max tokens: %v", err)
	}
	if outMax != inMax {
		t.Fatalf("max tokens round trip: got %+v want %+v", outMax, inMax)
	}

	inOverflow := StreamContextOverflowMsg{ChannelID: "e994", Window: 4096, LingID: "ling-2"}
	b, err = msgpack.Marshal(inOverflow)
	if err != nil {
		t.Fatalf("marshal overflow: %v", err)
	}
	var outOverflow StreamContextOverflowMsg
	if err := msgpack.Unmarshal(b, &outOverflow); err != nil {
		t.Fatalf("unmarshal overflow: %v", err)
	}
	if outOverflow != inOverflow {
		t.Fatalf("overflow round trip: got %+v want %+v", outOverflow, inOverflow)
	}
}
