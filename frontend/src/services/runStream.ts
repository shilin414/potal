/**
 * SSE client for the unified Run stream (GET /api/v2/runs/{id}/stream).
 *
 * SSE is transport only (architecture doc §61/§63): the stream closes or
 * breaks without implying run failure. On reconnect the gateway replays
 * persisted events from TiDB first, so the consumer can safely re-render
 * from sequence 0. Terminal events (run.completed/failed/cancelled) end
 * the subscription — run.retrying is deliberately NON-terminal: a
 * requeued attempt keeps the same stream alive (Execution Correctness
 * Closure).
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

export function openRunStream(
  runId: string,
  handlers: RunStreamHandlers,
): { close: () => void } {
  const controller = new AbortController();
  const baseUrl = import.meta.env.VITE_API_BASE_URL || '/api';
  let closed = false;
  let sawTerminal = false;

  const dispatch = (event: RunEventRecord) => {
    if (event.event_type && TERMINAL_EVENTS.has(event.event_type)) {
      sawTerminal = true;
    }
    handlers.onEvent(event);
  };

  (async () => {
    while (!closed && !sawTerminal) {
      let reader: ReadableStreamDefaultReader<Uint8Array> | null = null;
      try {
        const response = await fetch(`${baseUrl}/v2/runs/${runId}/stream`, {
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
      // replays all persisted events, so nothing is lost — just repeated.
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
  dispatch: (event: RunEventRecord) => void,
): void {
  const trimmed = frame.trim();
  if (!trimmed || trimmed.startsWith(':')) return;
  const dataLines: string[] = [];
  frame.split(/\r?\n/).forEach((line) => {
    // The gateway emits `event: run.event` frames; the payload itself
    // carries the unified event_type.
    if (line.startsWith('data:')) dataLines.push(line.slice(5).trimStart());
  });
  if (!dataLines.length) return;
  try {
    dispatch(JSON.parse(dataLines.join('\n')) as RunEventRecord);
  } catch {
    // Ignore malformed frames; persisted replay reconciles later.
  }
}
