package sse

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStreamProtocolNegotiationDegradesToLegacy pins the ONE rule that keeps a
// mixed-version deployment safe: anything that is not an explicit, parsable,
// in-range declaration of protocol 2 gets the legacy frame set.
//
// The direction matters. An absent parameter is the OLD frontend (protocol 1),
// and an unparsable value is a client bug — both must degrade to "durable
// chunks only" rather than erroring: a 400 here would break a client that
// cannot know the server was upgraded, and defaulting the other way would hand
// an append-only client the transient deltas it cannot reconcile. The failure
// modes are asymmetric, so the default is not a style choice.
func TestStreamProtocolNegotiationDegradesToLegacy(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"", StreamProtocolLegacy},
		{"after=12", StreamProtocolLegacy},
		{"stream_protocol=1", 1},
		{"stream_protocol=2", StreamProtocolRangeDelta},
		{"stream_protocol=3", 3}, // forward compatible: newer clients keep their claim
		{"stream_protocol=0", StreamProtocolLegacy},
		{"stream_protocol=-1", StreamProtocolLegacy},
		{"stream_protocol=abc", StreamProtocolLegacy},
		{"stream_protocol=", StreamProtocolLegacy},
		{"stream_protocol=%202", StreamProtocolRangeDelta},
		// The protocol and the durable cursor are independent dimensions:
		// negotiating one must not disturb the other.
		{"after=100&stream_protocol=2", StreamProtocolRangeDelta},
	}
	for _, tc := range cases {
		url := "http://example.test/v2/runs/abc/stream"
		if tc.query != "" {
			url += "?" + tc.query
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if got := StreamProtocol(req); got != tc.want {
			t.Errorf("StreamProtocol(%q) = %d, want %d", tc.query, got, tc.want)
		}
	}
}

// TestResumeCursorIgnoresStreamProtocol: Last-Event-ID and `stream_protocol`
// must not leak into each other. Encoding the protocol into the cursor would
// make a mid-life client upgrade resume from the wrong position.
func TestResumeCursorIgnoresStreamProtocol(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"http://example.test/v2/runs/abc/stream?after=77&stream_protocol=2", nil)
	if got := ResumeCursor(req); got != 77 {
		t.Fatalf("ResumeCursor with stream_protocol = %d, want 77", got)
	}
	req = httptest.NewRequest(http.MethodGet,
		"http://example.test/v2/runs/abc/stream?stream_protocol=2", nil)
	req.Header.Set("Last-Event-ID", "42")
	if got := ResumeCursor(req); got != 42 {
		t.Fatalf("Last-Event-ID with stream_protocol = %d, want 42", got)
	}
}
