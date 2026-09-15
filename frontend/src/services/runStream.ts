/**
 * SSE client for the unified Run stream (GET /api/v2/runs/{id}/stream).
 *
 * SSE is transport only (architecture doc §61/§63): the stream closes or
 * breaks without implying run failure.
 *
 * RESUMPTION (第九轮 P1-1/P1-2). The gateway tags every DURABLE frame with
 * `id: <run sequence>`, so this client keeps a durable cursor and reconnects
 * with `?after=<cursor>`. Only what the consumer has not already rendered is
 * replayed:
 *
 *   durable frame (sequence > 0) → advances the cursor
 *   transient frame (sequence 0)  → does NOT advance it (content.delta is
 *                                   fanned out live and never persisted, so
 *                                   "0" is a sentinel, not a position)
 *
 * Two guards make that safe:
 *
 *   1. `?after` — the server replays only sequences above the cursor, which
 *      is what stops a reconnect from re-rendering the whole answer;
 *   2. a local dedupe — anything at or below the cursor is dropped, covering
 *      the overlap between what was in flight when the transport died and
 *      what the replay re-sends.
 *
 * Guard 2 is not redundant. Durable chunks are INCREMENTAL (第九轮 P1-4), so a
 * replayed chunk that is fed to the reducer twice appends its text twice —
 * visibly duplicated output instead of a self-healing snapshot replace.
 *
 * Terminal events (run.completed/failed/cancelled) end the subscription —
 * run.retrying and run.waiting_external are deliberately NON-terminal: a
 * requeued or parked attempt keeps the same stream alive (Execution
 * Correctness Closure).
 */
import type { RunEventRecord } from '@/services/runApi';

export interface RunStreamHandlers {
  onEvent: (event: RunEventRecord) => void;
  onError?: (error: Error) => void;
  /** Transport ended without a terminal event (stream broke / timed out). */
  onTransportEnd?: () => void;
}

const TERMINAL_EVENTS = new Set([
  'run.completed', 'run.failed', 'run.cancelled',
]);

/**
 * durableSequenceOf resolves the run sequence a frame advances the cursor to.
 * The DATA body is authoritative (it is the same value the server writes on
 * the `id:` line); the framing is the fallback for a body that omits it.
 * Returns 0 for a transient frame — never a cursor position.
 */
function durableSequenceOf(event: RunEventRecord, sseId?: number): number {
  if (typeof event.sequence === 'number' && event.sequence > 0) return event.sequence;
  if (typeof sseId === 'number' && sseId > 0) return sseId;
  return 0;
}

export function openRunStream(
  runId: string,
  handlers: RunStreamHandlers,
): { close: () => void } {
  const controller = new AbortController();
  const baseUrl = import.meta.env.VITE_API_BASE_URL || '/api';
  let closed = false;
  let sawTerminal = false;
  // The highest durable sequence already dispatched to the consumer. It is
  // the ONLY state that survives a transport break, which is why it lives
  // here rather than in the store: the store may hold a different run.
  let lastDurableSequence = 0;

  const dispatch = (event: RunEventRecord, sseId?: number) => {
    const sequence = durableSequenceOf(event, sseId);
    const isTerminal = !!event.event_type && TERMINAL_EVENTS.has(event.event_type);
    if (sequence > 0 && sequence <= lastDurableSequence) {
      // Already delivered before the reconnection. A duplicate TERMINAL
      // frame still ends the loop (the run is over either way) but must not
      // be re-dispatched — the reducer has already rendered that outcome.
      if (isTerminal) sawTerminal = true;
      return;
    }
    if (sequence > 0) lastDurableSequence = sequence;
    if (isTerminal) sawTerminal = true;
    handlers.onEvent(event);
  };

  (async () => {
    while (!closed && !sawTerminal) {
      let reader: ReadableStreamDefaultReader<Uint8Array> | null = null;
      try {
        // Resume from the cursor: without it the gateway replays the run
        // from sequence 0 on every reconnect (the pre-第九轮 behaviour).
        const cursor = lastDurableSequence > 0 ? `?after=${lastDurableSequence}` : '';
        const response = await fetch(`${baseUrl}/v2/runs/${runId}/stream${cursor}`, {
          credentials: 'same-origin', // studio_session cookie
          signal: controller.signal,
        });
        if (!response.ok) {
          throw new Error(`stream HTTP ${response.status}`);
        }
        reader = response.body?.getReader() || null;
        if (!reader) throw new Error('stream has no body');
        const decoder = new TextDecoder();
        let buffer = '';
        let complete = false;
        while (!complete && !closed && !sawTerminal) {
          const { done, value } = await reader.read();
          complete = done;
          buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
          const frames = buffer.split(/\r?\n\r?\n/);
          buffer = frames.pop() || '';
          // A terminal event is a HARD boundary (第七轮 P2-2). HTTP,
          // ReadableStream, nginx and TCP may all coalesce several SSE
          // frames into ONE chunk, so the loop has to stop between frames —
          // forEach would keep feeding events that follow the terminal one
          // into the reducer (e.g. a stale/dirty run.failed followed by
          // run.completed) and rewrite an outcome the UI already rendered.
          for (const frame of frames) {
            if (closed || sawTerminal) break;
            consumeFrame(frame, dispatch);
          }
          if (complete && buffer.trim() && !closed && !sawTerminal) {
            consumeFrame(buffer, dispatch);
          }
        }
      } catch (error: any) {
        if (closed || error?.name === 'AbortError') return;
        handlers.onError?.(error instanceof Error ? error : new Error(String(error)));
      } finally {
        // cancel() returns a promise. When the terminal event closed the
        // stream (controller.abort() in close()) the body is already dead,
        // and the cancel promise rejects with "BodyStreamBuffer was
        // aborted" — swallow it: the stream is over either way, and an
        // unhandled rejection here surfaces as a console error after every
        // completed run with artifacts.
        try {
          reader?.cancel().catch(() => { /* already aborted/closed */ });
        } catch { /* reader already closed */ }
      }
      if (closed || sawTerminal) return;
      // Transport ended mid-run: retry after a short backoff. The gateway
      // replays persisted events ABOVE the cursor, so nothing is lost — and
      // unlike the old unconditional reclaim, nothing is repeated either.
      await new Promise((resolve) => setTimeout(resolve, 2000));
    }
    if (!closed && !sawTerminal) handlers.onTransportEnd?.();
  })();

  return {
    close: () => {
      closed = true;
      controller.abort();
    },
  };
}

function consumeFrame(
  frame: string,
  dispatch: (event: RunEventRecord, sseId?: number) => void,
): void {
  const trimmed = frame.trim();
  if (!trimmed || trimmed.startsWith(':')) return;
  const dataLines: string[] = [];
  let sseId: number | undefined;
  frame.split(/\r?\n/).forEach((line) => {
    // The gateway emits `event: run.event` frames; the payload itself
    // carries the unified event_type. The `id:` line carries the durable
    // run sequence (absent on transient frames).
    if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart());
    } else if (line.startsWith('id:')) {
      const parsed = Number.parseInt(line.slice(3).trim(), 10);
      if (Number.isFinite(parsed)) sseId = parsed;
    }
  });
  if (!dataLines.length) return;
  try {
    dispatch(JSON.parse(dataLines.join('\n')) as RunEventRecord, sseId);
  } catch {
    // Ignore malformed frames; persisted replay reconciles later.
  }
}
