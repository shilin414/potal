/**
 * useScheduleDetail — 失败域解耦 + 执行记录分页回归（三次复审 §33–§37）。
 *
 *   · 任务详情与执行记录是两个失败域：occurrences 接口挂了不能连累
 *     任务配置；
 *   · 执行记录 keyset 分页（51 probe / 50 display）：第 51 条以前的
 *     历史不再不可见，loadMore 以已展示最后一行 id 作 before_id；
 *   · 执行记录失败保留已加载记录，重试可恢复。
 * renderHook-style coverage via createRoot/act（本项目无 testing-library）。
 * @vitest-environment jsdom
 */
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';
import { useScheduleDetail, type UseScheduleDetailResult } from '../useScheduleDetail';
import {
  fetchSchedule,
  fetchScheduleOccurrences,
} from '@/services/scheduleApi';
import type { Schedule, ScheduleOccurrence } from '@/types/schedule';

vi.mock('@/services/scheduleApi', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  fetchSchedule: vi.fn(),
  fetchScheduleOccurrences: vi.fn(),
}));

const mockSchedule = vi.mocked(fetchSchedule);
const mockOccurrences = vi.mocked(fetchScheduleOccurrences);

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const schedule = (id: number): Schedule => ({
  id, name: `任务${id}`, description: '', application_id: 7,
  prompt: '总结今天的数据', schedule_type: 'daily', cron_expression: '',
  timezone: 'Asia/Shanghai', run_at: null,
  trigger: { time: '09:00', days_of_week: [1], day_of_month: 1 },
  enabled: true, conversation_policy: 'new_each_run', overlap_policy: 'queue',
  misfire_policy: 'fire_once', deadline_policy: 'execute_anyway',
  execution_window_seconds: 0, next_run_at: null, last_run_at: null,
  created_at: '', updated_at: '', deliveries: [],
});

const occ = (id: number): ScheduleOccurrence => ({
  id, schedule_id: 9, scheduled_at: '', status: 'success',
  enqueued_at: null, finished_at: null, run_id: null, deliveries: undefined,
} as unknown as ScheduleOccurrence);

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

interface Probe { current: UseScheduleDetailResult | null }

let host: HTMLElement;
let root: Root;
let latest: Probe;
let openRef: { current: boolean };
let idRef: { current: number | null };

function ProbeComponent() {
  latest.current = useScheduleDetail(openRef.current, idRef.current);
  return null;
}

beforeEach(() => {
  mockSchedule.mockReset();
  mockOccurrences.mockReset();
  latest = { current: null };
  openRef = { current: true };
  idRef = { current: 9 };
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => { root.unmount(); });
  host.remove();
});

describe('useScheduleDetail — 失败域解耦（§36–§37）', () => {
  it('an occurrences failure does NOT hide the schedule config', async () => {
    mockSchedule.mockResolvedValue(schedule(9));
    mockOccurrences.mockRejectedValue(new Error('历史接口故障'));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    // 任务配置正常加载…
    expect(latest.current!.schedule?.name).toBe('任务9');
    expect(latest.current!.error).toBeNull();
    // …执行记录独立报错，可重试。
    expect(latest.current!.occurrenceError).toBe('历史接口故障');
  });

  it('a schedule failure keeps the occurrence domain independent', async () => {
    mockSchedule.mockRejectedValue(new Error('配置接口故障'));
    mockOccurrences.mockResolvedValue([occ(3), occ(2)]);
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    expect(latest.current!.error).toBe('配置接口故障');
    expect(latest.current!.occurrences.map((o) => o.id)).toEqual([3, 2]);
    expect(latest.current!.occurrenceError).toBeNull();
  });

  it('the occurrences retry recovers after a failure', async () => {
    mockSchedule.mockResolvedValue(schedule(9));
    mockOccurrences
      .mockRejectedValueOnce(new Error('暂时不可用'))
      .mockResolvedValueOnce([occ(5)]);
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.occurrenceError).toBe('暂时不可用');

    await act(async () => { await latest.current!.retryOccurrences(); });
    await flush(10);
    expect(latest.current!.occurrenceError).toBeNull();
    expect(latest.current!.occurrences).toHaveLength(1);
  });

  it('the SCHEDULE retry recovers after a config failure (四次复审 P2-3)', async () => {
    mockSchedule
      .mockRejectedValueOnce(new Error('配置暂时不可用'))
      .mockResolvedValueOnce(schedule(9));
    mockOccurrences.mockResolvedValue([occ(3)]);
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.error).toBe('配置暂时不可用');
    expect(latest.current!.schedule).toBeNull();

    await act(async () => { await latest.current!.retrySchedule(); });
    await flush(10);
    expect(latest.current!.error).toBeNull();
    expect(latest.current!.schedule?.name).toBe('任务9');
    // 配置重试不影响已独立的执行记录域。
    expect(latest.current!.occurrenceError).toBeNull();
  });
});

describe('useScheduleDetail — 执行记录分页（§33–§35）', () => {
  it('51 probe / 50 display: shows 50 and flags hasMore', async () => {
    mockSchedule.mockResolvedValue(schedule(9));
    mockOccurrences.mockResolvedValue(
      Array.from({ length: 51 }, (_, i) => occ(51 - i)));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    expect(mockOccurrences).toHaveBeenCalledWith(9, undefined, 51);
    expect(latest.current!.occurrences).toHaveLength(50);
    expect(latest.current!.hasMoreOccurrences).toBe(true);
  });

  it('loadMore sends the last displayed id as before_id and appends', async () => {
    mockSchedule.mockResolvedValue(schedule(9));
    mockOccurrences
      .mockResolvedValueOnce(Array.from({ length: 51 }, (_, i) => occ(100 - i)))
      .mockResolvedValueOnce([occ(50), occ(49)]);
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.occurrences).toHaveLength(50);

    await act(async () => { await latest.current!.loadMoreOccurrences(); });
    await flush(10);

    expect(mockOccurrences).toHaveBeenLastCalledWith(9, 51, 51);
    expect(latest.current!.occurrences).toHaveLength(52);
    expect(latest.current!.hasMoreOccurrences).toBe(false);
  });

  it('a failed loadMore keeps the loaded history; retry appends', async () => {
    mockSchedule.mockResolvedValue(schedule(9));
    mockOccurrences
      .mockResolvedValueOnce(Array.from({ length: 51 }, (_, i) => occ(100 - i)))
      .mockRejectedValueOnce(new Error('翻页失败'))
      .mockResolvedValueOnce([occ(50)]);
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    await act(async () => { await latest.current!.loadMoreOccurrences(); });
    await flush(10);
    expect(latest.current!.occurrences).toHaveLength(50);
    expect(latest.current!.occurrenceError).toBe('翻页失败');

    await act(async () => { await latest.current!.loadMoreOccurrences(); });
    await flush(10);
    expect(latest.current!.occurrences).toHaveLength(51);
    expect(latest.current!.occurrenceError).toBeNull();
  });
});

describe('useScheduleDetail — 关闭清空（复审 P1）', () => {
  it('closing drops the config/history so a reopen never flashes them', async () => {
    mockSchedule.mockResolvedValue(schedule(9));
    mockOccurrences.mockResolvedValue([occ(3), occ(2)]);
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.schedule?.name).toBe('任务9');
    expect(latest.current!.occurrences).toHaveLength(2);

    // Close the drawer: the closed surface must hold NOTHING of the previous
    // target — otherwise reopening for another schedule paints 任务9's
    // config under the new title for the first frame.
    openRef.current = false;
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.schedule).toBeNull();
    expect(latest.current!.occurrences).toEqual([]);
    expect(latest.current!.occurrenceError).toBeNull();
    expect(latest.current!.hasMoreOccurrences).toBe(false);
    expect(latest.current!.error).toBeNull();
  });

  it('a late occurrences response cannot repopulate after close', async () => {
    mockSchedule.mockResolvedValue(schedule(9));
    let resolveOccs!: (value: ScheduleOccurrence[]) => void;
    mockOccurrences.mockImplementationOnce(
      () => new Promise<ScheduleOccurrence[]>((res) => { resolveOccs = res; }));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    openRef.current = false;
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.occurrences).toEqual([]);

    // The in-flight response settles now — it must be discarded entirely.
    await act(async () => { resolveOccs([occ(5), occ(4)]); });
    await flush(10);
    expect(latest.current!.occurrences).toEqual([]);
    expect(latest.current!.hasMoreOccurrences).toBe(false);
  });
});

describe('useScheduleDetail — loadMore 代际失效（四次复审 P1-2）', () => {
  it('a target switch releases the stale loadMore spinner immediately; the new target pages on its own', async () => {
    // A 的 loadMore 在途时切到任务 B：旧请求必须当场作废（spinner 立即
    // false，而不是等旧 HTTP 结束），B 能立刻翻自己的下一页；旧请求晚到时
    // 既不能覆盖 B 数据，也不能关掉 B loadMore 的 spinner。
    mockSchedule.mockResolvedValue(schedule(9));
    let resolveStale!: (value: ScheduleOccurrence[]) => void;
    let resolveB!: (value: ScheduleOccurrence[]) => void;
    mockOccurrences
      .mockResolvedValueOnce(Array.from({ length: 51 }, (_, i) => occ(100 - i))) // A 首页
      .mockImplementationOnce(() => new Promise<ScheduleOccurrence[]>((res) => {
        resolveStale = res;
      })) // A loadMore 在途
      .mockResolvedValueOnce(Array.from({ length: 51 }, (_, i) => occ(200 - i))) // B 首页
      .mockImplementationOnce(() => new Promise<ScheduleOccurrence[]>((res) => {
        resolveB = res;
      })); // B loadMore 在途
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.hasMoreOccurrences).toBe(true);

    const stale = latest.current!.loadMoreOccurrences();
    await flush(10);
    expect(latest.current!.loadingMoreOccurrences).toBe(true);

    // 在途时切到任务 B。
    idRef.current = 10;
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.occurrencesLoading).toBe(false);        // B 首页已落地
    expect(latest.current!.loadingMoreOccurrences).toBe(false);    // A 的旧 loadMore 已当场作废

    // B 可以立即翻自己的下一页（A 的请求仍在途）。
    const bMore = latest.current!.loadMoreOccurrences();
    await flush(10);
    expect(mockOccurrences).toHaveBeenLastCalledWith(10, 151, 51);
    expect(latest.current!.loadingMoreOccurrences).toBe(true);     // B 自己的 spinner

    // A 的旧响应此刻才返回：不得覆盖 B 数据、不得关掉 B 的 spinner。
    await act(async () => { resolveStale([occ(50), occ(49)]); });
    await act(async () => { await stale; });
    await flush(10);
    expect(latest.current!.occurrences.map((o) => o.id))
      .toEqual(Array.from({ length: 50 }, (_, i) => 200 - i));
    expect(latest.current!.loadingMoreOccurrences).toBe(true);     // B 的 spinner 仍在
    expect(latest.current!.occurrenceError).toBeNull();

    // B 的第二页落地，spinner 才复位。
    await act(async () => { resolveB([occ(150), occ(149)]); });
    await act(async () => { await bMore; });
    await flush(10);
    expect(latest.current!.loadingMoreOccurrences).toBe(false);
    expect(latest.current!.occurrences).toHaveLength(52);
  });
});
