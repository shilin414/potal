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

import { openRunStream } from '../runStream';

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
