/**
 * useSchedules — keyset 分页 + 服务端搜索回归（三次复审 P1 §17–§28）。
 *
 * 后端 /v2/schedules 回裸数组（没有 next_cursor），hasMore 由 51 probe /
 * 50 display 推导：请求 51 条 → 展示 50、hasMore；≤50 条 → 全展示、到底。
 * loadMore 以已展示最后一行 id 作 before_id；筛选/搜索变化重置回第一页。
 * 搜索 q 直发后端（300ms 防抖），覆盖全部任务而不是已加载页。
 * renderHook-style coverage via createRoot/act（本项目无 testing-library）。
 * @vitest-environment jsdom
 */
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';
import { useSchedules, type UseSchedulesResult } from '../useSchedules';
import { fetchSchedules } from '@/services/scheduleApi';
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

/** ids DESC：count 条、最大 id = maxId（模拟 ORDER BY id DESC）。 */
const page = (count: number, maxId: number) => (
  Array.from({ length: count }, (_, i) => schedule(maxId - i)));

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

interface Probe { current: UseSchedulesResult | null }

let host: HTMLElement;
let root: Root;
let latest: Probe;

function ProbeComponent() {
  latest.current = useSchedules();
  return null;
}

beforeEach(() => {
  mockFetch.mockReset();
  latest = { current: null };
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
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    expect(mockFetch).toHaveBeenCalledWith('all', undefined, 51, '');
    expect(latest.current!.data).toHaveLength(50);
    expect(latest.current!.data[0]!.id).toBe(51);
    expect(latest.current!.hasMore).toBe(true);
  });

  it('a short page (≤50) is shown whole with hasMore = false', async () => {
    mockFetch.mockResolvedValue(page(7, 7));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    expect(latest.current!.data).toHaveLength(7);
    expect(latest.current!.hasMore).toBe(false);
  });

  it('loadMore sends the last displayed id as before_id and appends the next page', async () => {
    mockFetch
      .mockResolvedValueOnce(page(51, 100))   // ids 100..50 → 显示 100..51
      .mockResolvedValueOnce(page(3, 50));    // 尾页 ids 50..48
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.data).toHaveLength(50);

    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);

    // 第二页以第一页最后一行（id 51）为 cursor。
    expect(mockFetch).toHaveBeenLastCalledWith('all', 51, 51, '');
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
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.hasMore).toBe(true);

    await act(async () => {
      latest.current!.setStatus('running');
    });
    await flush(10);

    expect(mockFetch).toHaveBeenLastCalledWith('running', undefined, 51, '');
    expect(latest.current!.data.map((s) => s.id)).toEqual([5, 4]);
    expect(latest.current!.hasMore).toBe(false);
  });

  it('reload re-requests page one for the current status', async () => {
    mockFetch
      .mockResolvedValueOnce(page(51, 60))
      .mockResolvedValueOnce(page(51, 61));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    await act(async () => { await latest.current!.reload(); });
    await flush(10);

    expect(mockFetch).toHaveBeenLastCalledWith('all', undefined, 51, '');
    expect(latest.current!.data).toHaveLength(50);
    expect(latest.current!.data[0]!.id).toBe(61);
  });

  it('a failed loadMore keeps the loaded rows for the next retry', async () => {
    mockFetch
      .mockResolvedValueOnce(page(51, 60))
      .mockRejectedValueOnce(new Error('网络中断'))
      .mockResolvedValueOnce(page(1, 10));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    // 已有 50 行仍在，错误可见，重试（再点 loadMore）可恢复。
    expect(latest.current!.data).toHaveLength(50);
    expect(latest.current!.error).toBe('网络中断');
    expect(latest.current!.errorPhase).toBe('loadMore');

    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    expect(latest.current!.data).toHaveLength(51);
    expect(latest.current!.error).toBeNull();
    expect(latest.current!.errorPhase).toBeNull();
  });
});

describe('useSchedules — 失败保留旧数据（三次复审 §30–§32）', () => {
  it('a reload failure keeps the loaded data and reports errorPhase = refresh', async () => {
    mockFetch
      .mockResolvedValueOnce(page(10, 30))
      .mockRejectedValueOnce(new Error('刷新失败'));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.data).toHaveLength(10);

    await act(async () => { await latest.current!.reload(); });
    await flush(10);

    // 已加载的 10 条真实数据仍在，错误是 refresh 相位。
    expect(latest.current!.data).toHaveLength(10);
    expect(latest.current!.error).toBe('刷新失败');
    expect(latest.current!.errorPhase).toBe('refresh');
    // 首页失败清 cursor：重试回第一页而不是继续旧 cursor。
    expect(latest.current!.hasMore).toBe(false);
  });

  it('a first-load failure with no data reports errorPhase = initial', async () => {
    mockFetch.mockRejectedValue(new Error('冷启动失败'));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    expect(latest.current!.data).toHaveLength(0);
    expect(latest.current!.error).toBe('冷启动失败');
    expect(latest.current!.errorPhase).toBe('initial');
  });

  it('a failed SEARCH keeps the previous rows (data does not vanish)', async () => {
    mockFetch
      .mockResolvedValueOnce(page(10, 30))
      .mockRejectedValueOnce(new Error('搜索失败'));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    await act(async () => { latest.current!.setSearch('库存'); });
    await flush(350); // 300ms 防抖到期 + 请求失败

    expect(latest.current!.data).toHaveLength(10);
    expect(latest.current!.error).toBe('搜索失败');
    expect(latest.current!.errorPhase).toBe('initial');
  });
});

describe('useSchedules — 服务端搜索（三次复审 §23–§28）', () => {
  it('the needle goes to the SERVER (q) after the debounce and resets to page one', async () => {
    mockFetch
      .mockResolvedValueOnce(page(51, 60))                          // 初始页
      .mockResolvedValueOnce([schedule(75, '月度库存分析')]);      // 搜索命中第 2 页任务
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(mockFetch).toHaveBeenCalledTimes(1);

    await act(async () => {
      latest.current!.setSearch('库存');
    });
    // 防抖窗口内没有新请求。
    await flush(100);
    expect(mockFetch).toHaveBeenCalledTimes(1);

    await flush(250); // 300ms 防抖到期
    expect(mockFetch).toHaveBeenLastCalledWith('all', undefined, 51, '库存');
    expect(latest.current!.data.map((s) => s.name)).toEqual(['月度库存分析']);
    // 搜索 = 新结果集：重置回第一页（无 before_id）。
    expect(latest.current!.hasMore).toBe(false);
  });

  it('loadMore carries the active search needle', async () => {
    mockFetch
      .mockResolvedValueOnce(page(51, 100))
      .mockResolvedValueOnce(page(51, 50));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    await act(async () => {
      latest.current!.setSearch('日报');
    });
    await flush(350);
    expect(mockFetch).toHaveBeenLastCalledWith('all', undefined, 51, '日报');

    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    // 搜索命中页展示 ids 50..1 → cursor 是最后一行 id=1，且带同一搜索词。
    expect(mockFetch).toHaveBeenLastCalledWith('all', 1, 51, '日报');
  });
});

describe('useSchedules — loadMore 代际失效（四次复审 P1-2）', () => {
  it('a NEW result set releases the stale loadMore spinner immediately, and the new set pages on its own', async () => {
    // 全部列表页 2 的 loadMore 卡在网络上；切到 running 后新首页落地 ——
    // 旧 loadMore 必须当场作废（spinner 立即 false，而不是等旧 HTTP 结束），
    // 且 running 能立刻翻自己的第二页；旧请求晚到时既不能覆盖 running 数据，
    // 也不能关掉 running 自己的 spinner。
    let resolveStale!: (value: Schedule[]) => void;
    let resolveB!: (value: Schedule[]) => void;
    mockFetch
      .mockResolvedValueOnce(page(51, 100))                                        // A(全部) 首页 ids 100..51
      .mockImplementationOnce(() => new Promise<Schedule[]>((res) => { resolveStale = res; })) // A loadMore 在途
      .mockResolvedValueOnce(page(51, 200))                                        // B(running) 首页 ids 200..150
      .mockImplementationOnce(() => new Promise<Schedule[]>((res) => { resolveB = res; }));    // B loadMore 在途
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.hasMore).toBe(true);

    const stale = latest.current!.loadMore();
    await flush(10);
    expect(latest.current!.loadingMore).toBe(true);

    // 切筛选：新结果集开始。新首页落地后 loadingMore 必须已经 false ——
    // 不能继承旧结果集的翻页生命周期（旧请求哪怕卡 60s 也不挡新列表）。
    await act(async () => { latest.current!.setStatus('running'); });
    await flush(10);
    expect(latest.current!.loading).toBe(false);       // B 首页已落地
    expect(latest.current!.loadingMore).toBe(false);   // 旧 loadMore 已当场作废

    // B 可以立即翻自己的第二页（旧 A 请求仍在途）。
    const bMore = latest.current!.loadMore();
    await flush(10);
    expect(mockFetch).toHaveBeenLastCalledWith('running', 151, 51, '');
    expect(latest.current!.loadingMore).toBe(true);    // 这是 B 自己的 spinner

    // 旧 A 的响应此刻才返回：不得覆盖 B 数据、不得关掉 B 的 spinner、
    // 不得给 B 设置任何错误。
    await act(async () => { resolveStale(page(3, 50)); });
    await act(async () => { await stale; });
    await flush(10);
    expect(latest.current!.data.map((s) => s.id))
      .toEqual(Array.from({ length: 50 }, (_, i) => 200 - i));
    expect(latest.current!.loadingMore).toBe(true);    // B 的 spinner 仍在
    expect(latest.current!.error).toBeNull();

    // B 的第二页正常落地，spinner 才复位。
    await act(async () => { resolveB(page(3, 150)); });
    await act(async () => { await bMore; });
    await flush(10);
    expect(latest.current!.loadingMore).toBe(false);
    expect(latest.current!.data).toHaveLength(53);
  });
});
