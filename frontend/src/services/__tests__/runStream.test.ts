/**
 * 第六轮: historical replay semantics of the run stream.
 *
 * These tests exist to pin down WHY migration 0018 could not be left as
 * "every run.interrupted becomes run.failed": the client stops the
 * subscription on the first terminal event, so a terminal frame that is
 * wrong (an interruption marker rewritten into a failure) makes the rest
 * of a successful run unreadable. run.retrying however is NON-terminal —
 * the same stream must keep reading across a requeue.
 */
import { describe, it, expect, vi, afterEach } from 'vitest';

import { openRunStream, STREAM_PROTOCOL_RANGE_DELTA } from '../runStream';

type Chunk = { data: string; done?: boolean };

function frame(eventType: string, sequence: number, payload: Record<string, unknown> = {}) {
  return `event: run.event\ndata: ${JSON.stringify({
    run_id: 'r1', sequence, event_type: eventType, payload,
  })}\n\n`;
}

/** A stream whose body yields the given chunks in order, then EOF. */
function mockFetchWithChunks(chunks: Chunk[], onRequest?: () => void) {
  const encoder = new TextEncoder();
  const fetchMock = vi.fn(async () => {
    onRequest?.();
    const remaining = chunks.slice();
    // One chunk per pull, so the consumer observes the framing boundary
    // between reads — the failure mode under test depends on it.
    const stream = new ReadableStream<Uint8Array>({
      pull(controller) {
        const next = remaining.shift();
        if (!next) {
          controller.close();
          return;
        }
        controller.enqueue(encoder.encode(next.data));
        if (next.done && !remaining.length) controller.close();
      },
    });
    return {
      ok: true,
      status: 200,
      body: stream,
    } as unknown as Response;
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('openRunStream historical replay', () => {
  it('keeps reading across run.retrying and only stops at run.completed', async () => {
    vi.useFakeTimers();
    const events: string[] = [];
    // Chunk 1: the historical interruption marker (non-terminal).
    // Chunk 2: the later success on the SAME stream.
    mockFetchWithChunks([
      { data: frame('run.retrying', 2, { reason: 'worker lease expired' }) },
      { data: frame('run.completed', 3, { status: 'succeeded' }) },
    ]);

    openRunStream('r1', { onEvent: (e) => events.push(e.event_type) });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);

    expect(events).toEqual(['run.retrying', 'run.completed']);
  });

  it('stops reading after run.failed (why the migration must not rewrite markers)', async () => {
    vi.useFakeTimers();
    const events: string[] = [];
    const requests: number[] = [];
    let n = 0;
    // First response carries a TERMINAL frame; the second one (which a
    // correct client never requests) would carry the real completion.
    mockFetchWithChunks(
      [
        { data: frame('run.failed', 2, {}) },
        { data: frame('run.completed', 3, {}) },
      ],
      () => requests.push(++n),
    );

    openRunStream('r1', { onEvent: (e) => events.push(e.event_type) });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    // Let the 2s reconnect backoff elapse: a terminal frame must not
    // trigger a reconnect, and must not let the later completed frame
    // through.
    await vi.advanceTimersByTimeAsync(5000);

    expect(events).toContain('run.failed');
    expect(events).not.toContain('run.completed');
    expect(requests.length).toBe(1);
  });

  it('treats run.cancelled as terminal (not a success)', async () => {
    vi.useFakeTimers();
    const events: string[] = [];
    mockFetchWithChunks([{ data: frame('run.cancelled', 2, { status: 'cancelled' }) }]);

    openRunStream('r1', { onEvent: (e) => events.push(e.event_type) });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);

    expect(events).toEqual(['run.cancelled']);
  });

  it('reconnects when the transport ends without a terminal event', async () => {
    vi.useFakeTimers();
    const events: string[] = [];
    const requests: number[] = [];
    let n = 0;
    mockFetchWithChunks(
      [{ data: frame('content.delta', 1, { text: 'partial' }) }],
      () => requests.push(++n),
    );

    const h = openRunStream('r1', {
      onEvent: (e) => events.push(e.event_type),
      onTransportEnd: () => events.push('transport.end'),
    });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    // Reconnect backoff (2s) must fire a second request.
    await vi.advanceTimersByTimeAsync(3000);
    h.close();

    expect(events).toContain('content.delta');
    expect(requests.length).toBeGreaterThanOrEqual(2);
  });
});

/**
 * 第七轮 P2-2: terminal is a HARD boundary inside a single network chunk.
 *
 * HTTP, ReadableStream, nginx and TCP may coalesce several SSE frames into
 * ONE read. The previous `frames.forEach` therefore kept dispatching
 * whatever followed the terminal frame — a stale/dirty `run.failed`
 * followed by `run.completed` would re-render a finished run as a success.
 */
describe('openRunStream same-chunk terminal boundary', () => {
  it('stops dispatching events after a terminal frame in the same network chunk', async () => {
    vi.useFakeTimers();
    const events: string[] = [];
    // ONE chunk, two frames — the framing boundary the consumer must honour.
    mockFetchWithChunks([
      { data: frame('run.failed', 2, {}) + frame('run.completed', 3, {}) },
    ]);

    openRunStream('r1', { onEvent: (e) => events.push(e.event_type) });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);

    expect(events).toEqual(['run.failed']);
  });

  it('emits both frames when a non-terminal frame precedes a terminal one in the same chunk', async () => {
    vi.useFakeTimers();
    const events: string[] = [];
    // run.retrying is NON-terminal: the same stream must keep reading
    // across a requeue, so the guard must not stop at it.
    mockFetchWithChunks([
      { data: frame('run.retrying', 2, { reason: 'worker lease expired' }) + frame('run.completed', 3, {}) },
    ]);

    openRunStream('r1', { onEvent: (e) => events.push(e.event_type) });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);

    expect(events).toEqual(['run.retrying', 'run.completed']);
  });
});

/**
 * 第九轮 P1-1/P1-2: the durable cursor.
 *
 * Reconnecting used to ask for the run from sequence 0 every time, so a long
 * answer was re-rendered from the beginning on every transport hiccup. The
 * gateway now tags durable frames with `id: <sequence>`, the client keeps the
 * highest sequence it has dispatched, and the resume must carry it — with a
 * local dedupe covering the overlap.
 *
 * The dedupe is not cosmetic once chunks are INCREMENTAL (P1-4): a replayed
 * chunk fed to the reducer twice appends its text twice, so the duplicate is
 * visible output rather than a harmless rewrite.
 */
describe('openRunStream durable cursor', () => {
  /** Each call returns the next scripted response; URLs are recorded. */
  function mockFetchSequence(responses: string[][], urls: string[]) {
    const encoder = new TextEncoder();
    let call = 0;
    const fetchMock = vi.fn(async (input: unknown) => {
      urls.push(String(input));
      const chunks = responses[Math.min(call, responses.length - 1)] || [];
      call += 1;
      const remaining = chunks.slice();
      const stream = new ReadableStream<Uint8Array>({
        pull(controller) {
          const next = remaining.shift();
          if (!next) {
            controller.close();
            return;
          }
          controller.enqueue(encoder.encode(next));
        },
      });
      return { ok: true, status: 200, body: stream } as unknown as Response;
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  it('reconnects from the highest durable sequence it already dispatched', async () => {
    vi.useFakeTimers();
    const urls: string[] = [];
    mockFetchSequence(
      [
        [frame('content.delta', 1, { text: 'a' }), frame('content.chunk', 2, { text: 'b' })],
        [frame('run.completed', 3, {})],
      ],
      urls,
    );

    const h = openRunStream('r1', { onEvent: () => {} });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(3000);
    h.close();

    expect(urls.length).toBeGreaterThanOrEqual(2);
    expect(urls[0]).not.toContain('after=');
    expect(urls[1]).toContain('after=2');
  });

  it('drops frames at or below the cursor instead of rendering them twice', async () => {
    vi.useFakeTimers();
    const urls: string[] = [];
    const events: string[] = [];
    // The second response deliberately repeats 1 and 2 (what a replay from a
    // stale cursor would look like) before the terminal frame.
    mockFetchSequence(
      [
        [frame('content.delta', 1, { text: 'a' }), frame('content.chunk', 2, { text: 'b' })],
        [
          frame('content.delta', 1, { text: 'a' }),
          frame('content.chunk', 2, { text: 'b' }),
          frame('run.completed', 3, {}),
        ],
      ],
      urls,
    );

    const h = openRunStream('r1', {
      onEvent: (e) => events.push(`${e.event_type}:${e.sequence}`),
    });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(3000);
    h.close();

    expect(events).toEqual([
      'content.delta:1',
      'content.chunk:2',
      'run.completed:3',
    ]);
  });

  it('does not advance the cursor for a transient frame (sequence 0)', async () => {
    vi.useFakeTimers();
    const urls: string[] = [];
    // A transient frame is fanned out live and never persisted: its sequence
    // is a SENTINEL, not a position. The dangerous ordering is durable →
    // transient: if the sentinel were allowed to move the cursor it would
    // RESET it, and the reconnect would ask the server to resume from 0 —
    // re-rendering the whole answer. (A transient arriving first, when the
    // cursor is still 0, changes nothing either way, so it would not test
    // anything.)
    mockFetchSequence(
      [
        [frame('content.chunk', 1, { text: 'durable' }), frame('content.delta', 0, { text: 'live only' })],
        [frame('run.completed', 2, {})],
      ],
      urls,
    );

    const h = openRunStream('r1', { onEvent: () => {} });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(3000);
    h.close();

    expect(urls.length).toBeGreaterThanOrEqual(2);
    expect(urls[1]).toContain('after=1');
  });

  it('falls back to the SSE id line when the body carries no sequence', async () => {
    vi.useFakeTimers();
    const urls: string[] = [];
    const idOnly =
      'event: run.event\nid: 7\n'
      + `data: ${JSON.stringify({
        run_id: 'r1', event_type: 'content.chunk', payload: { text: 'c' },
      })}\n\n`;
    mockFetchSequence([[idOnly], [frame('run.completed', 8, {})]], urls);

    const h = openRunStream('r1', { onEvent: () => {} });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(3000);
    h.close();

    expect(urls[1]).toContain('after=7');
  });

  it('still stops at a repeated terminal frame without re-dispatching it', async () => {
    vi.useFakeTimers();
    const urls: string[] = [];
    const events: string[] = [];
    mockFetchSequence(
      [[frame('content.delta', 1, { text: 'a' })], [frame('run.completed', 2, {})]],
      urls,
    );

    const h = openRunStream('r1', { onEvent: (e) => events.push(e.event_type) });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(3000);
    // The run ended: no further reconnect may be attempted.
    await vi.advanceTimersByTimeAsync(10000);
    h.close();

    expect(events.filter((e) => e === 'run.completed')).toHaveLength(1);
    expect(urls).toHaveLength(2);
  });
});

/**
 * 第九轮补丁 3.3-B: SSE stream protocol capability negotiation.
 *
 * Transient `content.delta` frames are only safe for a client that reconciles
 * them against durable chunks by byte offset — the gateway may deliver the
 * durable chunk FIRST, and an append-only client renders ABCABC. The client
 * therefore declares the capability on the request, and the server decides
 * which frame set to send.
 *
 * The declaration must be on EVERY connection, not just the first: it is a
 * rendering capability, so folding it into the durable cursor (or letting it
 * ride on Last-Event-ID) would leave a mid-life reconnect rendering under a
 * protocol the server no longer knows about.
 */
describe('openRunStream protocol capability', () => {
  function mockFetchSequence(responses: string[][], urls: string[]) {
    const encoder = new TextEncoder();
    let call = 0;
    const fetchMock = vi.fn(async (input: unknown) => {
      urls.push(String(input));
      const chunks = responses[Math.min(call, responses.length - 1)] || [];
      call += 1;
      const remaining = chunks.slice();
      const stream = new ReadableStream<Uint8Array>({
        pull(controller) {
          const next = remaining.shift();
          if (!next) {
            controller.close();
            return;
          }
          controller.enqueue(encoder.encode(next));
        },
      });
      return { ok: true, status: 200, body: stream } as unknown as Response;
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  it('declares stream_protocol=2 on the very first connection', async () => {
    vi.useFakeTimers();
    const urls: string[] = [];
    mockFetchSequence([[frame('run.completed', 1, {})]], urls);

    const h = openRunStream('r1', { onEvent: () => {} });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    h.close();

    expect(urls).toHaveLength(1);
    // Without this the server must assume an append-only client and withhold
    // the transient delta — the low-latency path would be silently dead.
    expect(urls[0]).toContain(`stream_protocol=${STREAM_PROTOCOL_RANGE_DELTA}`);
  });

  it('declares the capability again on a reconnect, alongside the cursor', async () => {
    vi.useFakeTimers();
    const urls: string[] = [];
    mockFetchSequence(
      [[frame('content.chunk', 5, { text: 'a', offset: 1 })], [frame('run.completed', 6, {})]],
      urls,
    );

    const h = openRunStream('r1', { onEvent: () => {} });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(3000);
    h.close();

    expect(urls.length).toBeGreaterThanOrEqual(2);
    expect(urls[1]).toContain('after=5');
    expect(urls[1]).toContain(`stream_protocol=${STREAM_PROTOCOL_RANGE_DELTA}`);
  });
});
