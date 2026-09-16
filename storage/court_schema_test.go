package storage

import (
	"strings"
	"testing"
)

// TestJSONScan_ToleratesMalformed verifies a bad payload row degrades to a
// bounded marker instead of hard-erroring a multi-row scan (edict 857).
func TestJSONScan_ToleratesMalformed(t *testing.T) {
	// Valid input still unmarshals normally.
	var good JSON
	if err := good.Scan([]byte(`{"a":1}`)); err != nil {
		t.Fatalf("valid scan returned error: %v", err)
	}
	if good["a"] != float64(1) {
		t.Fatalf("valid scan of Payload = %#v, want a=1", good)
	}

	// A malformed value must not error the scan; it yields the marker.
	raw := []byte("{\"answer\": \"line one\nline two\"}")
	var bad JSON
	if err := bad.Scan(raw); err != nil {
		t.Fatalf("malformed scan returned a hard error: %v", err)
	}
	marker, ok := bad[MalformedMarkerKey].(string)
	if !ok || marker == "" {
		t.Fatalf("malformed scan of Payload = %#v, want %q marker", bad, MalformedMarkerKey)
	}

	// Nil stays nil and is not treated as malformed.
	var nilVal JSON
	if err := nilVal.Scan(nil); err != nil || nilVal != nil {
		t.Fatalf("nil scan = (%v, %#v), want (nil, nil)", err, nilVal)
	}
}

// TestJSONScan_MalformedDoesNotLeakRawValue verifies the scan neither returns
// the full raw payload in an error nor in the marker.
func TestJSONScan_MalformedDoesNotLeakRawValue(t *testing.T) {
	raw := []byte(`{"x": "` + strings.Repeat("z", 1000) + "\n" + `"}`)
	var j JSON
	err := j.Scan(raw)
	if err != nil && strings.Contains(err.Error(), string(raw)) {
		t.Fatalf("error leaked the full raw payload: %v", err)
	}
	marker, ok := j[MalformedMarkerKey].(string)
	if !ok {
		t.Fatalf("expected a string %q marker, got %#v", MalformedMarkerKey, j)
	}
	// Bounded to the marker limit (plus the truncation ellipsis), so a huge
	// poisoned value cannot bloat the marker or log output.
	if len([]rune(marker)) > malformedMarkerLimit+1 {
		t.Fatalf("marker length = %d runes, want at most %d", len([]rune(marker)), malformedMarkerLimit+1)
	}
}

// TestStringArrayScan_ToleratesMalformed verifies the same resilience for
// array-shaped rows (e.g. lings.dependencies).
func TestStringArrayScan_ToleratesMalformed(t *testing.T) {
	// Valid input still parses.
	var good StringArray
	if err := good.Scan([]byte(`["a","b"]`)); err != nil {
		t.Fatalf("valid scan returned error: %v", err)
	}
	if len(good) != 2 {
		t.Fatalf("valid scan = %#v, want 2 elements", good)
	}

	// Malformed input yields nil, not a hard error.
	var bad StringArray
	if err := bad.Scan([]byte(`["a",]`)); err != nil {
		t.Fatalf("malformed scan returned a hard error: %v", err)
	}
	if bad != nil {
		t.Fatalf("malformed scan = %#v, want nil", bad)
	}

	// A malformed value must not be echoed back in the returned error.
	raw := []byte(`["` + strings.Repeat("q", 500) + "\n")
	if err := bad.Scan(raw); err != nil && strings.Contains(err.Error(), string(raw)) {
		t.Fatalf("error leaked the full raw value: %v", err)
	}

	// Drivers may hand back a string rather than []byte.
	var fromStr StringArray
	if err := fromStr.Scan(`["x"]`); err != nil {
		t.Fatalf("string scan returned error: %v", err)
	}
	if len(fromStr) != 1 || fromStr[0] != "x" {
		t.Fatalf("string scan = %#v, want [x]", fromStr)
	}
}
