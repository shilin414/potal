import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api } from '@/services/api';
import { useAuthStore } from '@/stores/useAuthStore';
import { createWsClient, type WsClient } from '@/services/wsClient';

export interface JobItem { id: string; name: string; status: string; result: string; error: string; }
export interface JobState {
  status: 'pending' | 'running' | 'done' | 'error' | 'stopped';
  progress: { current: number; total: number; label: string };
  items: JobItem[];
  logs: { level: string; msg: string }[];
}

const INITIAL: JobState = {
  status: 'pending',
  progress: { current: 0, total: 0, label: '' },
  items: [],
  logs: [],
};

const LOG_CAP = 500;

/** Pure reducer — unit-tested without React. */
export function reduceJobEvent(state: JobState, event: any): JobState {
  switch (event.type) {
    case 'job.state':
      return { ...state, status: event.status };
    case 'job.progress':
      return { ...state, progress: { current: event.current, total: event.total, label: event.label } };
    case 'item.state': {
      const next = state.items.filter((i) => i.id !== event.id);
      next.push({ id: event.id, name: event.name, status: event.status, result: event.result ?? '', error: event.error ?? '' });
      return { ...state, items: next };
    }
    case 'log': {
      const logs = [...state.logs, { level: event.level ?? 'info', msg: event.msg ?? '' }];
      return { ...state, logs: logs.slice(-LOG_CAP) };
    }
    case 'state.snapshot':
      return {
        status: event.status ?? state.status,
        progress: event.progress ?? state.progress,
        items: event.items ?? [],
        logs: (event.logs ?? []).slice(-LOG_CAP),
      };
    default:
      return state;
  }
}

function wsBaseUrl(): string {
  // Vite proxies /ws -> backend in dev; in prod put WS behind the same origin.
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/ws`;
}

export interface UseJobResult {
  state: JobState;
  jobId: string | null;
  start: (config: Record<string, any>) => Promise<void>;
  stop: () => Promise<void>;
}

/** Reusable hook: POST a job, open a WS, reduce events into UI state. */
export function useJob(appSlug: string): UseJobResult {
  const [state, setState] = useState<JobState>(INITIAL);
  const [jobId, setJobId] = useState<string | null>(null);
  const wsRef = useRef<WsClient | null>(null);

  // Close the websocket when the component using this hook unmounts.
  useEffect(() => {
    return () => {
      wsRef.current?.close();
      wsRef.current = null;
    };
  }, []);

  const start = useCallback(async (config: Record<string, any>) => {
    setState(INITIAL);
    const data = await api.post<{ id: string }>('/app-runner/jobs/', { app_slug: appSlug, config });
    const id = data.id;
    setJobId(id);

    const token = useAuthStore.getState().token ?? '';
    const url = `${wsBaseUrl()}/runner/${id}/?token=${encodeURIComponent(token)}`;
    wsRef.current?.close();
    wsRef.current = createWsClient(url, {
      onMessage: (event) => setState((prev) => reduceJobEvent(prev, event)),
    });
  }, [appSlug]);

  const stop = useCallback(async () => {
    if (jobId) await api.post(`/app-runner/jobs/${jobId}/stop/`);
  }, [jobId]);

  return useMemo(() => ({ state, jobId, start, stop }), [state, jobId, start, stop]);
}
