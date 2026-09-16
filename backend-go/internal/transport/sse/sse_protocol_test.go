package sse

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// TestStreamProtocolNegotiationDegradesToLegacy pins the ONE rule that keeps a
// mixed-version deployment safe: anything that is not an explicit, parsable,
// positive declaration of protocol 2 gets the legacy frame set.
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
		{"stream_protocol=1", StreamProtocolLegacy},
		{"stream_protocol=2", StreamProtocolRangeDelta},
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

// TestStreamProtocolNegotiatesFutureVersionsDown pins 第九轮补丁 3.3.1-B: the
// negotiated protocol is a SERVER capability, not an echo of the request.
//
// A newer client may legitimately ask for protocol 3 and must keep working, so
// the request is not rejected — but the answer is the highest protocol this
// server implements (2), because that is what it will actually render. Echoing
// the request instead would (a) claim a wire contract no v3 defines and (b)
// turn the value into a Prometheus label, so any authenticated client could
// mint a new time series per distinct integer. These cases all collapse onto
// the two protocols the server can render.
//
// The overflow case matters as much as the junk case: `strconv.Atoi` reports
// ErrRange there, and a fail-safe parser must treat "too large to be a
// protocol" exactly like "not a number" rather than panicking or wrapping.
func TestStreamProtocolNegotiatesFutureVersionsDown(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"stream_protocol=3", StreamProtocolRangeDelta},    // forward compatible, clamped
		{"stream_protocol=4", StreamProtocolRangeDelta},    // …
		{"stream_protocol=100", StreamProtocolRangeDelta},  // …
		{"stream_protocol=1001", StreamProtocolRangeDelta}, // the cardinality attack
		{"stream_protocol=999999", StreamProtocolRangeDelta},
		{"stream_protocol=2147483647", StreamProtocolRangeDelta},          // math.MaxInt32
		{"stream_protocol=99999999999999999999999", StreamProtocolLegacy}, // Atoi overflow
	} {
		req := httptest.NewRequest(http.MethodGet,
			"http://example.test/v2/runs/abc/stream?"+tc.query, nil)
		got := StreamProtocol(req)
		if got != tc.want {
			t.Errorf("StreamProtocol(%q) = %d, want %d", tc.query, got, tc.want)
		}
		if got != StreamProtocolLegacy && got != StreamProtocolRangeDelta {
			t.Errorf("StreamProtocol(%q) = %d, want a negotiated value in {%d,%d}",
				tc.query, got, StreamProtocolLegacy, StreamProtocolRangeDelta)
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

// TestSSEStreamProtocolMetricCardinalityIsBounded is the test that matters most
// for 第九轮补丁 3.3.1-B, because the real defect was not the parsed number — it
// was the LABEL. `Stream()` feeds the negotiated protocol straight into
//
//	g.Metrics.SSEStreamProtocolTotal.WithLabelValues(strconv.Itoa(protocol))
//
// and a CounterVec mints a time series per distinct label value. While the raw
// request value was echoed back, any authenticated client that can open its own
// run stream could walk ?stream_protocol=1001, 1002, 1003, … and grow the
// Prometheus registry without bound — a scrape/TSDB problem that no amount of
// parser correctness fixes.
//
// So this test does not check the parser in isolation; it replays the EXACT
// sequence Stream() performs (negotiate → use the result as the label) for a
// hostile set of inputs, then gathers the registry and asserts the family holds
// at most the two negotiated series. A parser that returns the request value
// fails here even if every unit assertion above still passes.
//
// The test lives in this package rather than in telemetry because both halves
// of the invariant — the negotiation and the label write — are only visible
// together here. The telemetry-side half (the label SHAPE) is pinned by
// TestSSEStreamProtocolMetricLabelShape in the telemetry package.
func TestSSEStreamProtocolMetricCardinalityIsBounded(t *testing.T) {
	m := telemetry.NewMetrics("test")

	// Ordered so the assertion below is meaningful: the inputs a cardinality
	// attack would use come first, then the junk that must fail safe.
	inputs := []string{
		"", "1", "2",
		"3", "4", "5", "100", "1001", "1002", "1003", "999999", "2147483647",
		"99999999999999999999999", "abc", "-1", "0",
	}
	for _, raw := range inputs {
		url := "http://example.test/v2/runs/abc/stream"
		if raw != "" {
			url += "?stream_protocol=" + raw
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)

		// Exactly what Stream() does.
		protocol := StreamProtocol(req)
		m.SSEStreamProtocolTotal.WithLabelValues(strconv.Itoa(protocol)).Inc()
	}

	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	var series int
	seen := map[string]bool{}
	for _, f := range families {
		if f.GetName() != "studio_sse_stream_protocol_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			series++
			for _, lp := range metric.GetLabel() {
				if lp.GetName() != "protocol" {
					t.Errorf("unexpected label %q on studio_sse_stream_protocol_total", lp.GetName())
					continue
				}
				seen[lp.GetValue()] = true
			}
		}
	}

	// Two series at most — one per protocol this server can actually render.
	if series > 2 {
		t.Fatalf("studio_sse_stream_protocol_total has %d series for %d distinct requests: "+
			"the protocol label is unbounded (a request parameter is minting time series)", series, len(inputs))
	}
	if series == 0 {
		t.Fatal("studio_sse_stream_protocol_total exported no series at all")
	}
	for value := range seen {
		if value != strconv.Itoa(StreamProtocolLegacy) && value != strconv.Itoa(StreamProtocolRangeDelta) {
			t.Fatalf("studio_sse_stream_protocol_total has protocol=%q, want only %q or %q",
				value, strconv.Itoa(StreamProtocolLegacy), strconv.Itoa(StreamProtocolRangeDelta))
		}
	}
	t.Logf("studio_sse_stream_protocol_total series=%d labels=%v (inputs=%d)",
		series, seen, len(inputs))
}
