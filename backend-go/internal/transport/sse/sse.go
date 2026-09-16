// Package sse implements the run event stream gateway:
//
//  1. Register the connection with its run's Hub, which owns the SINGLE Redis
//     pub/sub subscription for that run inside this process.
//  2. Replay persisted RunEvents from the client's DURABLE CURSOR (not
//     necessarily from 0) — from the Hub's bounded cache when it can prove
//     continuity for that cursor, otherwise from MySQL, page by page —
//     stopping at the first terminal event.
//  3. Drain the subscriber's live queue (live frames buffered while step 2
//     ran), skipping anything the replay already delivered.
//  4. Keepalive comment after 15s of silence.
//  5. Close as soon as a terminal event is observed — in the replay, in
//     the live queue, or synthesized from an already-settled run status.
//
// A terminal event is a HARD boundary (第七轮 P1): replay stops at the
// first one and the handler returns regardless of what the caller's
// `run.Status` snapshot said. The snapshot was read before the stream
// opened, so it can still say `running` while the replay already carries
// the terminal event — trusting the snapshot kept the connection open
// (keepalive forever) after the client had been told the run ended.
//
// Resumability (第九轮 P1-1/P1-2): every DURABLE frame carries an SSE `id:`
// equal to its run sequence, so a reconnect can ask to resume from the last
// event it actually processed:
//
//	GET /v2/runs/{id}/stream?after=123
//	Last-Event-ID: 123            (set automatically by EventSource)
//
// Precedence is query > Last-Event-ID > 0: an explicit cursor is a deliberate
// client decision, while Last-Event-ID is whatever the browser last
// remembered.
//
// A TRANSIENT frame (sequence 0 — content.delta is fanned out live without
// being persisted) must NOT write an `id:`. Sequence 0 is a sentinel, not a
// position: writing it would make the browser's next Last-Event-ID "0" and
// silently restart every replay from the beginning, re-rendering the whole
// answer.
//
// DURABLE ORDERING (Batch 4.1). This gateway guarantees that the durable
// frames written to ONE connection are monotonically CONTIGUOUS — 101 then
// 102, never 102 then 101 and never a hole. That is a PROMISE TO THE CLIENT,
// not an implementation detail: a resumable client derives its reconnect
// cursor from the highest sequence it has rendered, so a consumer that
// received 102 and never 101 would ask for `after=102` on its next connect
// and lose 101 permanently — and a consumer that received a terminal event
// out of order would stop reading before its predecessors arrived.
//
// The guarantee cannot be delegated to the publisher. Redis pub/sub delivery
// order is publish order, and a durable event is published AFTER its
// transaction commits, so two writers can commit 101 then 102 and publish in
// the opposite order. MySQL is the ordering authority; see repairDurableGap.
//
// PROTOCOL CAPABILITY (第九轮补丁 3.3-B). Transient `content.delta` frames are
// only safe to render for a client that can reconcile them against the durable
// `content.chunk` frames that carry the same text:
//
//	GET /v2/runs/{id}/stream?stream_protocol=2
//
// A protocol-2 client reassembles by UTF-8 BYTE RANGE (`content.delta.offset`
// and `content.chunk.offset` share one coordinate system), so an overlapping
// transient/durable pair renders once. A legacy client has no offset and can
// only append — and because the gateway can observe a durable chunk BEFORE the
// buffered transient delta for the same bytes (reverse order is normal), an
// append-only client renders "ABCABC".
//
// Negotiation, not duplicated reconciliation: a client that does not declare
// protocol 2 simply does not receive transient deltas. It still receives every
// durable `content.chunk` (written within ~500ms / ~2KB of the bytes being
// produced), so the worst case is a slightly coarser streaming cadence — never
// a lost or duplicated answer.
//
// Batch 4 (SSE Hub) keeps that contract byte-for-byte and moves the capability
// DOWN into the subscriber: the Hub fans out to N connections with N different
// protocols, so `content.delta` is filtered per Subscriber (see
// Subscriber.accepts). The protocol decision still happens in exactly one
// place per connection — this handler — and it is still opaque to the client.
//
// The declaration is a REQUEST, not a decision (第九轮补丁 3.3.1-B): the server
// answers with the highest protocol it implements, so `stream_protocol=3` (or
// any larger positive integer a future client might send) is served as
// protocol 2 rather than echoed back. See StreamProtocol.
//
// Frame format (validated frontend contract):
//
//	event: run.event\n
//	id: 123\n                       (durable frames only)
//	data: {"run_id","sequence","event_type","payload"[,"created_at"]}\n\n
package sse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

const (
	defaultKeepalive = 15 * time.Second
)

// Stream protocol capabilities.
//
// These two values are also the ONLY values the negotiated protocol may take:
// StreamProtocolRangeDelta is this server's highest implemented version, and
// StreamProtocol clamps every request down to it. See the note there for why
// the raw request value is never echoed back.
const (
	// StreamProtocolLegacy is what a client that sends no `stream_protocol`
	// (or an unparsable / non-positive / overflowing value) gets: durable
	// frames only.
	StreamProtocolLegacy = 1
	// StreamProtocolRangeDelta declares that the client understands
	// `content.delta.offset` and reconciles transient deltas against durable
	// chunks by UTF-8 byte range. Only such a client receives transient
	// `content.delta` frames. It is also the highest protocol this server
	// implements, so it is the ceiling of every negotiation.
	StreamProtocolRangeDelta = 2
)

// StreamProtocolHeader advertises the protocol the response was rendered for.
// It is for browser Network panels, logs and canary triage only — no client
// logic may depend on it (the request parameter is the contract).
const StreamProtocolHeader = "X-Studio-Stream-Protocol"

// RunReader is the execution-service surface this package needs: one keyset
// page of a run's durable events.
//
// It is an interface for one reason — testability of the part that loses data
// when it is wrong. The replay/live handover, the terminal boundary and the
// cache-miss fallback are only observable through the paged reader, and pinning
// them should not require a MySQL instance. `*execution.Service` is the
// production implementation and the only one wired outside tests.
type RunReader interface {
	ListEventPage(ctx context.Context, runID ids.ID, after uint64, limit int) (*execution.EventPage, error)
}

// Gateway serves GET /api/v2/runs/{id}/stream for a single run.
//
// It holds NO Redis client (Batch 4 §7). That is a structural guarantee, not a
// style preference: while this struct could subscribe to Redis directly, some
// future fallback branch would eventually restore "one Redis subscription per
// connection" — the exact regression the Hub exists to prevent. The only way
// to reach Redis from here is through the Hub, which owns at most one
// subscription per run.
type Gateway struct {
	Runs      RunReader
	Hub       *HubManager
	Keepalive time.Duration // defaults to 15s
	Metrics   *telemetry.Metrics
}

// StreamProtocol resolves the client's declared SSE rendering capability and
// NEGOTIATES it against what this server can actually render.
//
// An absent, unparsable, non-positive or overflowing value degrades to the
// LEGACY behaviour rather than failing the stream: the safe direction of a
// capability negotiation is the smaller feature set, and a legacy client that
// receives a 400 here would be broken by a server upgrade it cannot see. The
// parameter is deliberately NOT carried in Last-Event-ID — the durable cursor
// and the rendering capability are different dimensions, and a capability
// that only arrives on reconnect would leave a mid-life upgrade with the
// wrong wire format on one connection.
//
// The return value is the NEGOTIATED protocol, never the raw request value
// (第九轮补丁 3.3.1-B). Protocol numbers are a server capability: the highest
// one this server implements is StreamProtocolRangeDelta, so a future client
// asking for 3 — or 100, or 2147483647, or anything a script can generate —
// gets 2 back, the largest version both sides understand. Echoing the request
// would claim a rendering contract that does not exist, and because the
// result is also a Prometheus label value it would let any authenticated
// client mint unbounded time series (`protocol="1001"`, `"1002"`, …) from a
// query parameter. The negotiated value is therefore always 1 or 2.
func StreamProtocol(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("stream_protocol"))
	if raw == "" {
		return StreamProtocolLegacy
	}
	// strconv.Atoi returns ErrRange for values that do not fit in an int; those
	// take the same fail-safe path as junk.
	requested, err := strconv.Atoi(raw)
	if err != nil || requested < StreamProtocolRangeDelta {
		return StreamProtocolLegacy
	}
	return StreamProtocolRangeDelta
}

// ResumeCursor resolves the client's durable cursor. query `after` wins over
// the `Last-Event-ID` header (see the package comment); an unparsable value
// degrades to 0 rather than failing the stream — the worst case is a full
// replay, which is exactly the pre-第九轮 behaviour.
func ResumeCursor(r *http.Request) uint64 {
	if raw := r.URL.Query().Get("after"); raw != "" {
		if v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64); err == nil {
			return v
		}
	}
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64); err == nil {
			return v
		}
	}
	return 0
}

// Stream writes the SSE response. The caller has already authenticated the
// user and verified run ownership.
func (g *Gateway) Stream(w http.ResponseWriter, r *http.Request, run *execution.Run) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// The NEGOTIATED rendering capability — the largest protocol both this
	// server and the client understand, i.e. always 1 or 2 (3.3.1-B). It is
	// never the raw `stream_protocol` request value: the metric label below
	// and the response header are both derived from it, so an echoed request
	// would make a query parameter an unbounded Prometheus label. In Batch 4
	// this one value becomes the SUBSCRIBER's capability — never the Hub's.
	protocol := StreamProtocol(r)
	if g.Metrics != nil {
		// The v1/v2 connection ratio is what the 3.3 rollout watches while old
		// and new frontends coexist: while protocol 1 is still non-zero the
		// backend must keep serving durable-only clients.
		g.Metrics.SSEStreamProtocolTotal.WithLabelValues(strconv.Itoa(protocol)).Inc()
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	// Observability only (browser Network panel, logs, canary triage) — the
	// client must not branch on it; the request parameter is the contract.
	h.Set(StreamProtocolHeader, strconv.Itoa(protocol))
	w.WriteHeader(http.StatusOK)
	// Flush the response headers immediately. Go buffers them until the first
	// write or flush, so without this a stream that has nothing to send yet
	// (a fresh run with no persisted events) leaves the client waiting for the
	// response — the connection is only observably established when the first
	// frame happens to arrive.
	flusher.Flush()

	ctx := r.Context()

	writeFrame := func(sequence uint64, eventType string, payload map[string]any, withCreated bool) bool {
		data := map[string]any{
			"run_id":     run.ID.String(),
			"sequence":   sequence,
			"event_type": eventType,
			"payload":    payload,
		}
		if withCreated {
			data["created_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		}
		raw, err := json.Marshal(data)
		if err != nil {
			return true
		}
		// The id line is what makes a reconnect able to resume instead of
		// replaying the whole run; it is written ONLY for durable frames.
		var frame bytes.Buffer
		frame.WriteString("event: run.event\n")
		if sequence > 0 {
			fmt.Fprintf(&frame, "id: %d\n", sequence)
		}
		frame.WriteString("data: ")
		frame.Write(raw)
		frame.WriteString("\n\n")
		if _, err := w.Write(frame.Bytes()); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	writeComment := func() bool {
		if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	after := ResumeCursor(r)

	// Register BEFORE replaying (§9). From the moment Subscribe returns, every
	// live frame — durable chunks AND transient deltas — lands in this
	// connection's queue. Replaying first would open a window in which a live
	// delta for bytes far ahead of the replay position is written to the
	// socket first: the client's rendered byte offset would jump forward and
	// the historical bytes in between would be discarded as an old range,
	// leaving a hole in the middle of the answer.
	hub, sub := g.attachHub(ctx, run, protocol)
	if sub != nil {
		defer sub.Close(DropReasonClientGone)
	}

	// lastDelivered starts at the client's cursor, not at 0: a live frame at
	// or below the cursor was already delivered to this client before the
	// reconnect, so re-sending it would duplicate content the client has
	// rendered. Replay below only ever raises it.
	//
	// From Batch 4.1 on it means something stronger than "replay position":
	// it is the highest CONTIGUOUS durable sequence this connection has been
	// sent (AC-4.1-6). The live loop below advances it only for
	// `lastDelivered+1` and fills forward holes from MySQL instead of skipping
	// them — the reconnect cursor a client derives from these frames is the
	// highest sequence it RENDERED, so a skipped frame is unrecoverable.
	lastDelivered := after
	cursor := after

	// Phase 1: the Hub's bounded durable cache, but ONLY when it can prove
	// continuity for this cursor (AC-8). A cache miss costs one MySQL read; a
	// wrong hit would silently lose every event in the gap, so "not proven"
	// is treated as "miss", never as "probably covered".
	if hub != nil {
		if events, covered := hub.ReplayAfter(after); covered {
			for _, ev := range events {
				if !writeFrame(ev.Sequence, ev.EventType, ev.Payload, false) {
					return
				}
				cursor = ev.Sequence
				lastDelivered = ev.Sequence
				if g.Metrics != nil {
					g.Metrics.SSEHubCacheReplayTotal.Inc()
				}
				if ev.IsTerminal() {
					// The stream has already delivered the end of the run.
					hub.NoteTerminal(ev.Sequence)
					return
				}
			}
		} else if g.Metrics != nil {
			g.Metrics.SSEHubCacheMissTotal.Inc()
		}
	}

	// Phase 2: MySQL replay, PAGE BY PAGE from the durable cursor.
	//
	// When phase 1 served the client, this is the tail reconciliation: it
	// starts at the cache's high-water mark and therefore also picks up
	// events that are already committed in MySQL but whose Redis publish has
	// not reached the hub yet. When phase 1 missed, it is the full replay.
	//
	// A single unbounded read used to load a run's entire history into
	// memory before writing the first frame (第九轮 P1-3): a run with 100k
	// events paid for all of them up front, and the client waited. Paging
	// bounds both memory and time-to-first-frame, and the terminal-event
	// hard boundary stops the walk as soon as the run's log ends.
	replayedTerminal := false
	for {
		page, err := g.Runs.ListEventPage(ctx, run.ID, cursor, execution.DefaultEventPageSize)
		if err != nil {
			return
		}
		for _, ev := range page.Items {
			if !writeFrame(ev.Sequence, ev.EventType, ev.Payload, false) {
				return
			}
			cursor = ev.Sequence
			lastDelivered = ev.Sequence
			if g.Metrics != nil {
				g.Metrics.SSEReplayEventsTotal.Inc()
			}
			if hub != nil {
				// Warm the cache for the next viewer. RememberDurable never
				// fans out — these events are already on their way through
				// the upstream, and delivering them twice is the duplicate
				// the durable cursor exists to prevent.
				hub.RememberDurable(ev.Sequence, ev.EventType, ev.Payload)
			}
			if execution.IsTerminalEventName(ev.EventType) {
				// A terminal event ends the run's lifecycle, so it is the
				// LAST frame this stream may carry — even if the table
				// holds rows after it (dirty history: a pre-0020
				// failed-then-completed shape). Stopping here also makes
				// the terminal fact the replay itself observed, instead
				// of the stale run.Status snapshot below.
				replayedTerminal = true
				break
			}
		}
		if replayedTerminal || !page.HasMore {
			break
		}
	}
	if replayedTerminal {
		// The stream has already delivered the end of the run. Returning
		// here is what makes the contract "server closes after the
		// terminal event" hold: the live loop would otherwise wait on
		// Redis forever, and the terminal message it is waiting for has
		// already been deduplicated by the `sequence <= lastDelivered`
		// guard (第七轮 P1 — normal outcome race, no bad data required).
		if hub != nil {
			hub.NoteTerminal(cursor)
		}
		return
	}
	if execution.IsSettled(run.Status) {
		// Belt-and-suspenders (修复计划 §19 fallback): the finalize
		// transaction guarantees terminal CAS + terminal event together;
		// if a LEGACY row predating that transaction is terminal without
		// a terminal event, synthesize the frame from the run status so
		// reconnecting clients still terminate their stream instead of
		// hammering a stream that closes immediately.
		if !replayedTerminal {
			synthetic := map[string]any{"status": run.Status}
			if t, ok := run.Output["text"].(string); ok && t != "" {
				synthetic["text"] = t
			}
			if run.ErrorCode != "" {
				synthetic["error_code"] = run.ErrorCode
			}
			if run.ErrorMessage != "" {
				synthetic["error_message"] = run.ErrorMessage
			}
			eventType, ok := syntheticTerminalEventName(run.Status)
			if !ok {
				// A settled status without a terminal event name is a
				// status nobody defined: never fabricate a frame (a
				// default of run.completed would report an unknown
				// outcome as a success).
				return
			}
			writeFrame(0, eventType, synthetic, true)
		}
		return
	}
	if sub == nil {
		// No live capability for this request: the run is live but the hub
		// could not carry events (the manager is closed, the upstream failed,
		// or SUBSCRIBE was not confirmed in time). The durable replay above is
		// complete and correct; ending here makes the client reconnect — with
		// its durable cursor — instead of hanging on a stream that can never
		// advance.
		return
	}

	keepaliveAfter := g.Keepalive
	if keepaliveAfter <= 0 {
		keepaliveAfter = defaultKeepalive
	}
	keepalive := time.NewTicker(keepaliveAfter)
	defer keepalive.Stop()

	// Phase 3: drain the subscriber queue. Live frames that arrived during the
	// replay are already buffered here, in the Hub's arrival order, so nothing
	// is lost between the two phases and nothing is written twice.
	//
	// The protocol filter is NOT here any more: it lives at the subscriber
	// (Subscriber.accepts), because it is a per-connection capability while
	// this loop shares one Hub with connections of both protocols.
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Done():
			// The hub dropped this subscriber — slow consumer, hub closed,
			// upstream lost. The client reconnects with its durable cursor.
			return
		case ev, ok := <-sub.Events():
			if !ok {
				return
			}
			// Release the queue's byte budget for every dequeued event,
			// including the ones skipped as duplicates below: holding it until
			// Close would make a long-lived connection look slow.
			sub.Release(ev)
			// A transient frame is a transport marker, not a position: it
			// never touches the durable cursor and is never repaired.
			if ev.IsTransient() {
				if !writeFrame(0, ev.EventType, ev.Payload, true) {
					return
				}
				continue
			}
			if ev.Sequence <= lastDelivered {
				// A duplicate, or a stale publish that arrived after its own
				// newer neighbour (see repairDurableGap for why that is
				// normal). Re-sending it would make the client render the
				// same content twice.
				continue
			}
			if ev.Sequence == lastDelivered+1 {
				if !writeFrame(ev.Sequence, ev.EventType, ev.Payload, true) {
					return
				}
				lastDelivered = ev.Sequence
				if ev.IsTerminal() {
					return
				}
				continue
			}
			// FORWARD GAP: sequence > lastDelivered+1. Writing it here would
			// advance this client's durable cursor past bytes it never saw —
			// and because the browser's cursor is the highest sequence it has
			// RENDERED, the skipped frames could never be recovered by a
			// reconnect either. Repair from the canonical log instead.
			//
			// `lastDelivered` therefore means "the highest CONTIGUOUS durable
			// sequence written to this connection", not "the highest sequence
			// observed" (AC-4.1-6).
			newLast, terminal, ok := g.repairDurableGap(ctx, run, hub, lastDelivered, ev.Sequence, writeFrame)
			if !ok {
				// Fail closed: end the transport and let the client
				// reconnect from its last contiguous cursor. Losing the
				// connection costs a reconnect; guessing here loses content.
				return
			}
			lastDelivered = newLast
			if terminal {
				return
			}
		case <-keepalive.C:
			if !writeComment() {
				return
			}
		}
	}
}

// repairDurableGap fills a FORWARD hole in the durable stream from the
// canonical event log, writing [from+1 .. through] in order.
//
// Redis is a low-latency NOTIFICATION channel, not an ordering authority. Two
// writers allocate 101 and 102 under the run row's serialization point, commit
// in that order, and can still PUBLISH in the other one — the publish happens
// after COMMIT, so a goroutine descheduled between the two lands late. Redis
// therefore legitimately delivers 102 before 101.
//
// The receive side cannot just skip 101: the client's reconnect cursor is the
// highest sequence it has RENDERED, so a skipped frame is lost for good, not
// merely for this connection (and if the late frame were the terminal one, the
// client would stop reading before its predecessor ever arrived).
//
// MySQL can always answer the question, and this is the one place where the
// two orderings are reconciled:
//
//   - `through` has been PUBLISHED, which means its transaction COMMITTED,
//     which means its sequence was ALLOCATED — and allocation is serialized
//     on the run row, so every lower sequence had already committed and
//     released that lock. Anything at or below `through` is already readable
//     here. Nothing is guessed: the range is read, page by page, from the log
//     that defines the order.
//   - `from` is the client's contiguous position, so the range is exactly the
//     hole and nothing else is re-read.
//
// It returns the new contiguous position. Fail-closed is the whole point of
// the (last, terminal, ok) shape: on ANY failure nothing after `from` was
// safely delivered, ok is false, and the caller ends the connection so the
// client reconnects and replays normally. A duplicate frame is a cosmetic
// defect; a dropped durable event is lost content, so the repair never
// continues past a hole it could not fill.
func (g *Gateway) repairDurableGap(
	ctx context.Context,
	run *execution.Run,
	hub *RunHub,
	from uint64,
	through uint64,
	writeFrame func(sequence uint64, eventType string, payload map[string]any, withCreated bool) bool,
) (last uint64, terminal bool, ok bool) {
	last = from
	for last < through {
		page, err := g.Runs.ListEventPage(ctx, run.ID, last, execution.DefaultEventPageSize)
		if err != nil {
			g.countLiveGapRepair(telemetry.LiveGapFailed)
			return from, false, false
		}
		progressed := false
		for _, ev := range page.Items {
			if ev.Sequence <= last {
				continue
			}
			if ev.Sequence > through {
				// The page ran past the event whose arrival exposed the
				// hole. That frame is written by the caller on the next
				// iteration; stopping exactly at `through` keeps this repair
				// from delivering anything twice.
				break
			}
			// `withCreated` is false: the timestamp is a property of the
			// frame's ARRIVAL at the transport, and this frame arrived in
			// the past — the replay phase below writes its frames the same
			// way.
			if !writeFrame(ev.Sequence, ev.EventType, ev.Payload, false) {
				// The socket is gone. Not a repair failure (nothing was
				// skipped), so it is not counted as one.
				return last, false, false
			}
			last = ev.Sequence
			progressed = true
			// A gap repair IS a MySQL replay, and SSEReplayEventsTotal is
			// already scoped to exactly that: durable events read from the
			// log and written to a client. SSELiveGapRepairTotal records WHY
			// this extra read happened.
			if g.Metrics != nil {
				g.Metrics.SSEReplayEventsTotal.Inc()
			}
			if hub != nil {
				// Warm the cache for the next viewer, exactly as the replay
				// phase does — and never fan out, for the same reason.
				hub.RememberDurable(ev.Sequence, ev.EventType, ev.Payload)
			}
			if execution.IsTerminalEventName(ev.EventType) {
				// The terminal is a hard boundary even when it arrives
				// through a repair: everything after it is history the run
				// did not produce. Without this the trigger frame — say
				// run.completed at 102 — would be delivered while 101, its
				// predecessor, was skipped (AC-4.1-4).
				if hub != nil {
					hub.NoteTerminal(ev.Sequence)
				}
				g.countLiveGapRepair(telemetry.LiveGapRepaired)
				return last, true, true
			}
		}
		if !progressed || !page.HasMore {
			break
		}
	}
	if last != through {
		// The log could not supply a contiguous range up to the frame whose
		// arrival exposed the hole. That should not happen (§11), but "should
		// not happen" is not a delivery guarantee: end the connection and let
		// the client's next replay decide, rather than passing the hole on.
		g.countLiveGapRepair(telemetry.LiveGapFailed)
		return from, false, false
	}
	g.countLiveGapRepair(telemetry.LiveGapRepaired)
	return last, false, true
}

func (g *Gateway) countLiveGapRepair(result string) {
	if g.Metrics == nil {
		return
	}
	g.Metrics.SSELiveGapRepairTotal.WithLabelValues(result).Inc()
}

// attachHub resolves the hub for this request and registers a subscriber.
//
// It returns a nil hub/subscriber pair whenever the connection must be served
// by a durable replay only:
//
//   - no hub manager is wired (tests, or a process without Redis);
//   - the run is ALREADY SETTLED and no retained hub exists — a finished run
//     must never pay for a Redis subscription (§20);
//   - the hub exists but cannot serve (upstream failed, SUBSCRIBE not
//     confirmed in time, or it was evicted between the lookup and the
//     registration).
//
// A settled run still REUSES a hub the run created while it was live (§21):
// the terminal hub keeps its cache for IdleTTL, so a reconnect in that window
// is served without touching MySQL — but no new upstream is ever opened for
// it, because `Lookup` never creates.
func (g *Gateway) attachHub(ctx context.Context, run *execution.Run, protocol int) (*RunHub, *Subscriber) {
	if g.Hub == nil {
		return nil, nil
	}
	settled := execution.IsSettled(run.Status)
	runID := run.ID.String()

	// Two attempts: a hub can be evicted (idle timer, upstream failure)
	// between the lookup and the registration. Recreating one is cheap while
	// the run is live; a settled run simply keeps the durable-only path.
	for attempt := 0; attempt < 2; attempt++ {
		hub := g.Hub.Lookup(runID)
		if hub == nil {
			if settled {
				return nil, nil
			}
			hub = g.Hub.GetOrCreate(runID)
			if hub == nil {
				return nil, nil
			}
		}
		// Wait for the upstream registration to settle before replaying, so
		// the "SUBSCRIBE first, replay second" property still holds: any
		// event published after this point is either in this connection's
		// queue or already visible to the replay's MySQL read.
		if !hub.WaitReady(ctx) || !hub.Serving() {
			return nil, nil
		}
		if sub, ok := hub.Subscribe(protocol); ok {
			return hub, sub
		}
	}
	return nil, nil
}

// syntheticTerminalEventName maps a SETTLED run status to the terminal
// frame the gateway synthesizes when the run has no persisted terminal
// event (修复计划 §19 fallback; legacy rows predating the atomic
// finalize transaction).
//
// cancelled must map to run.cancelled — mapping it to run.completed, as
// the previous inline `if failed||interrupted` did, resurrects exactly
// the "cancelled ≠ success" bug the execution closure removed: the
// frontend renders a user-cancelled run as a successful one.
//
// `interrupted` is the pre-closure terminal ALIAS of `failed` (第五轮
// P2-1) and therefore still maps to run.failed for rows migration 0018
// has not normalized yet.
//
// The bool is false for any non-settled or unknown status: the caller
// must NOT fabricate a terminal frame then, because a zero-value event
// name would either be ignored by the client (stream never closes) or,
// with a naive default, reported as a success nobody observed.
func syntheticTerminalEventName(status string) (string, bool) {
	switch status {
	case execution.StatusSucceeded:
		return execution.EventRunCompleted, true
	case execution.StatusCancelled:
		return execution.EventRunCancelled, true
	case execution.StatusFailed, execution.StatusInterrupted:
		return execution.EventRunFailed, true
	}
	return "", false
}
