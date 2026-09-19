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

describe('useSchedules — loadMore 失效复位（复审意见 #2）', () => {
  it('a loadMore invalidated by a filter change still clears loadingMore', async () => {
    // loadMore 在途时筛选变化 bump 了 seq：旧响应必须作废，且 loadingMore
    // 必须复位 —— 否则「加载更多」按钮永久楔死（曾用 seq 守卫 finally 的缺陷）。
    let resolveStale!: (value: Schedule[]) => void;
    mockFetch
      .mockResolvedValueOnce(page(51, 100))                                        // 初始页 ids 100..51
      .mockImplementationOnce(() => new Promise<Schedule[]>((res) => { resolveStale = res; })) // 在途 loadMore
      .mockResolvedValueOnce(page(2, 200));                                        // 筛选变化后的新首页
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.hasMore).toBe(true);

    const more = latest.current!.loadMore();
    await flush(10);
    expect(latest.current!.loadingMore).toBe(true);

    // 筛选变化：bump seq + 新首页请求。
    await act(async () => { latest.current!.setStatus('running'); });
    await flush(10);
    expect(latest.current!.loading).toBe(false);     // 新首页已落地
    expect(latest.current!.loadingMore).toBe(true);  // 旧 loadMore 仍在途

    await act(async () => { resolveStale(page(3, 50)); });
    await act(async () => { await more; });
    await flush(10);
    // 旧页被丢弃，且 loadingMore 复位（按钮不再楔死）。
    expect(latest.current!.loadingMore).toBe(false);
    expect(latest.current!.data.map((s) => s.id)).toEqual([200, 199]);
  });
});
