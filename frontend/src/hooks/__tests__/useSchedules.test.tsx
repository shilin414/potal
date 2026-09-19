/**
 * useSchedules — keyset 分页回归（三次复审 P1 §17–§22）。
 *
 * 后端 /v2/schedules 回裸数组（没有 next_cursor），hasMore 由 51 probe /
 * 50 display 推导：请求 51 条 → 展示 50、hasMore；≤50 条 → 全展示、到底。
 * loadMore 以已展示最后一行 id 作 before_id；筛选变化重置回第一页。
 * renderHook-style coverage via createRoot/act（本项目无 testing-library）。
 * @vitest-environment jsdom
 */
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';
import { useSchedules, type UseSchedulesResult } from '../useSchedules';
import {
  deleteSchedule,
  disableSchedule,
  enableSchedule,
  fetchSchedules,
  runScheduleNow,
} from '@/services/scheduleApi';
import type { Schedule } from '@/types/schedule';

vi.mock('@/services/scheduleApi', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  fetchSchedules: vi.fn(),
  enableSchedule: vi.fn(),
  disableSchedule: vi.fn(),
  runScheduleNow: vi.fn(),
  deleteSchedule: vi.fn(),
}));

const mockFetch = vi.mocked(fetchSchedules);

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const schedule = (id: number, name = `任务${id}`): Schedule => ({
  id, name, description: '', application_id: 7, prompt: 'p',
  schedule_type: 'daily', cron_expression: '', timezone: 'Asia/Shanghai',
  run_at: null,
  trigger: { time: '09:00', days_of_week: [1], day_of_month: 1 },
  enabled: true, conversation_policy: 'new_each_run', overlap_policy: 'queue',
  misfire_policy: 'fire_once', deadline_policy: 'execute_anyway',
  execution_window_seconds: 0, next_run_at: null, last_run_at: null,
  created_at: '', updated_at: '', deliveries: [],
});

/** ids 1..n，DESC 顺序模拟后端 ORDER BY id DESC。 */
const page = (count: number, maxId: number) => (
  Array.from({ length: count }, (_, i) => schedule(maxId - i)));

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

interface Probe { current: UseSchedulesResult | null }

function mountProbe(latest: Probe) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  function ProbeComponent() {
    latest.current = useSchedules();
    return null;
  }
  return { host, root, element: React.createElement(ProbeComponent) };
}

let host: HTMLElement;
let root: Root;

beforeEach(() => {
  mockFetch.mockReset();
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => { root.unmount(); });
  host.remove();
});

describe('useSchedules — 51 probe / 50 display 分页', () => {
  it('requests 51, shows only 50 and flags hasMore when a 51st exists', async () => {
    mockFetch.mockResolvedValue(page(51, 51));
    const latest: Probe = { current: null };
    await act(async () => { root.render(mountProbe(latest).element); });
    await flush(10);

    expect(mockFetch).toHaveBeenCalledWith('all', undefined, 51);
    expect(latest.current!.data).toHaveLength(50);
    expect(latest.current!.data[0]!.id).toBe(51);
    expect(latest.current!.hasMore).toBe(true);
  });

  it('a short page (≤50) is shown whole with hasMore = false', async () => {
    mockFetch.mockResolvedValue(page(7, 7));
    const latest: Probe = { current: null };
    await act(async () => { root.render(mountProbe(latest).element); });
    await flush(10);

    expect(latest.current!.data).toHaveLength(7);
    expect(latest.current!.hasMore).toBe(false);
  });

  it('loadMore sends the last displayed id as before_id and appends the next page', async () => {
    mockFetch
      .mockResolvedValueOnce(page(51, 100))   // ids 100..50 → 显示 100..51
      .mockResolvedValueOnce(page(3, 50));    // 尾页 ids 50..48
    const latest: Probe = { current: null };
    await act(async () => { root.render(mountProbe(latest).element); });
    await flush(10);
    expect(latest.current!.data).toHaveLength(50);

    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);

    // 第二页以第一页最后一行（id 51）为 cursor。
    expect(mockFetch).toHaveBeenLastCalledWith('all', 51, 51);
    expect(latest.current!.data).toHaveLength(53);
    expect(latest.current!.data.map((s) => s.id)).toEqual([
      ...Array.from({ length: 50 }, (_, i) => 100 - i),
      50, 49, 48,
    ]);
    expect(latest.current!.hasMore).toBe(false);
  });

  it('a status change resets to page one (no before_id)', async () => {
    mockFetch
      .mockResolvedValueOnce(page(51, 60))
      .mockResolvedValueOnce(page(2, 5));
    const latest: Probe = { current: null };
    await act(async () => { root.render(mountProbe(latest).element); });
    await flush(10);
    expect(latest.current!.hasMore).toBe(true);

    await act(async () => {
      latest.current!.setStatus('running');
    });
    await flush(10);

    expect(mockFetch).toHaveBeenLastCalledWith('running', undefined, 51);
    expect(latest.current!.data.map((s) => s.id)).toEqual([5, 4]);
    expect(latest.current!.hasMore).toBe(false);
  });

  it('reload re-requests page one for the current status', async () => {
    mockFetch
      .mockResolvedValueOnce(page(51, 60))
      .mockResolvedValueOnce(page(51, 61));
    const latest: Probe = { current: null };
    await act(async () => { root.render(mountProbe(latest).element); });
    await flush(10);

    await act(async () => { await latest.current!.reload(); });
    await flush(10);

    expect(mockFetch).toHaveBeenLastCalledWith('all', undefined, 51);
    expect(latest.current!.data).toHaveLength(50);
    expect(latest.current!.data[0]!.id).toBe(61);
  });

  it('a failed loadMore keeps the loaded rows for the next retry', async () => {
    mockFetch
      .mockResolvedValueOnce(page(51, 60))
      .mockRejectedValueOnce(new Error('网络中断'))
      .mockResolvedValueOnce(page(1, 10));
    const latest: Probe = { current: null };
    await act(async () => { root.render(mountProbe(latest).element); });
    await flush(10);

    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    // 已有 50 行仍在，错误可见，重试（再点 loadMore）可恢复。
    expect(latest.current!.data).toHaveLength(50);
    expect(latest.current!.error).toBe('网络中断');

    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    expect(latest.current!.data).toHaveLength(51);
    expect(latest.current!.error).toBeNull();
  });
});
