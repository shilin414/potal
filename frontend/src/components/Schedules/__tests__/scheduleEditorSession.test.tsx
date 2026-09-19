/**
 * useScheduleEditor — 保存会话生命周期回归（四次复审 P1-3）。
 *
 * 编辑器保存没有任何 target/session 守卫时：任务 A 的保存慢返回会继续
 * message.success / onSaved / onClose —— 把用户刚打开的任务 B 的新编辑器
 * 直接关掉；且 saving 是同一份 Hook state，B 会继承 A 的 spinner。
 *
 * 会话身份 = (open, editing?.id, presetApplicationId)：任一变化即新会话
 * （editorEpochRef），旧会话的保存响应对新 UI 一律无效；后端 PATCH 本身
 * 无法撤销，守卫的边界是「旧响应不操纵新会话的 UI」。
 */
// @vitest-environment jsdom
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

import type { Schedule } from '@/types/schedule';

const mocks = vi.hoisted(() => ({
  fetchApplicationPage: vi.fn(),
  resolveApplication: vi.fn(),
  updateSchedule: vi.fn(),
  createSchedule: vi.fn(),
  previewScheduleRuns: vi.fn(),
}));

vi.mock('@/services/runApi', () => ({
  fetchApplicationPage: mocks.fetchApplicationPage,
  resolveApplication: mocks.resolveApplication,
}));

vi.mock('@/services/shareApi', () => ({
  fetchFeishuTargets: vi.fn(async () => ({ items: [], next_cursor: '', has_more: false })),
}));

vi.mock('@/services/scheduleApi', () => ({
  createSchedule: mocks.createSchedule,
  updateSchedule: mocks.updateSchedule,
  previewScheduleRuns: mocks.previewScheduleRuns,
  fetchSchedule: vi.fn(async () => ({})),
  fetchScheduleOccurrences: vi.fn(async () => []),
  enableSchedule: vi.fn(),
  disableSchedule: vi.fn(),
  runScheduleNow: vi.fn(),
  deleteSchedule: vi.fn(),
}));

import { ScheduleEditorModal } from '../ScheduleEditorModal';
import { updateSchedule } from '@/services/scheduleApi';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};
(globalThis as unknown as { matchMedia: unknown }).matchMedia = (query: string) => ({
  matches: false, media: query, onchange: null,
  addListener() {}, removeListener() {},
  addEventListener() {}, removeEventListener() {},
  dispatchEvent: () => false,
});

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

function click(el: Element) {
  return act(async () => {
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

/** antd 按钮文本可能带换行/空格 —— 统一压平后比较。 */
const btnText = (el: Element) => (el.textContent || '').replace(/\s+/g, '');

function saveButton(): HTMLButtonElement | undefined {
  return Array.from(document.querySelectorAll<HTMLButtonElement>(
    '.ant-modal-footer .ant-btn')).find((b) => btnText(b) === '保存');
}

/** Deferred promise — 由测试决定响应何时落地。 */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const schedule = (id: number, applicationId: number): Schedule => ({
  id, name: `任务${id}`, description: '', application_id: applicationId,
  prompt: '总结今天的数据', schedule_type: 'daily', cron_expression: '',
  timezone: 'Asia/Shanghai', run_at: null,
  trigger: { time: '09:00', days_of_week: [1], day_of_month: 1 },
  enabled: true, conversation_policy: 'new_each_run', overlap_policy: 'queue',
  misfire_policy: 'fire_once', deadline_policy: 'execute_anyway',
  execution_window_seconds: 0, next_run_at: null, last_run_at: null,
  created_at: '', updated_at: '', deliveries: [],
});

const SCHEDULE_A = schedule(9, 888);
const SCHEDULE_B = schedule(10, 889);

beforeEach(() => {
  mocks.fetchApplicationPage.mockReset().mockResolvedValue({
    items: [], next_cursor: '', has_more: false,
  });
  mocks.resolveApplication.mockReset().mockImplementation(
    async ({ id }: { id: number }) => ({ id, name: `智能体${id}` }));
  mocks.updateSchedule.mockReset();
  mocks.createSchedule.mockReset();
  mocks.previewScheduleRuns.mockReset().mockResolvedValue([]);
});

describe('useScheduleEditor — 保存会话守卫（四次复审 P1-3）', () => {
  it('a save settling after close→reopen cannot close the NEW editor (ABA)', async () => {
    const saveA = deferred<{ id: number }>();
    mocks.updateSchedule.mockReturnValueOnce(saveA.promise);
    const onClose = vi.fn();
    const onSaved = vi.fn();

    const host = document.createElement('div');
    document.body.appendChild(host);
    const root = createRoot(host);
    // 会话 #1：编辑任务 A，保存 pending。
    await act(async () => {
      root.render(
        <ScheduleEditorModal open editing={SCHEDULE_A} onClose={onClose} onSaved={onSaved} />,
      );
    });
    await flush(50);
    const save = saveButton()!;
    await click(save);
    await flush(20);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledWith(9, expect.anything());
    expect(saveButton()!.className).toContain('ant-btn-loading'); // A 保存 spinner

    // 用户关闭编辑器（父组件置 open=false —— 不是 onClose），再打开任务 B。
    await act(async () => {
      root.render(
        <ScheduleEditorModal open={false} editing={null} onClose={onClose} onSaved={onSaved} />,
      );
    });
    await flush(20);
    await act(async () => {
      root.render(
        <ScheduleEditorModal open editing={SCHEDULE_B} onClose={onClose} onSaved={onSaved} />,
      );
    });
    await flush(50);

    // 新会话（任务 B）已就绪：编辑器打开、保存按钮不再 spinner ——
    // 不继承任务 A 的保存生命周期。
    expect(saveButton()).toBeTruthy();
    expect(saveButton()!.className).not.toContain('ant-btn-loading');

    // 任务 A 的保存此刻才返回：不得 toast/onSaved/onClose 把 B 的编辑器关掉。
    await act(async () => { saveA.resolve({ id: 9 }); });
    await flush(20);
    expect(onSaved).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(document.body.textContent).not.toContain('定时任务已更新');
    expect(saveButton()).toBeTruthy(); // B 的编辑器仍然打开
    expect(saveButton()!.className).not.toContain('ant-btn-loading');

    await act(async () => { root.unmount(); });
    host.remove();
  });

  it('switching the editing target releases saving immediately — the new editor can save without waiting', async () => {
    const saveA = deferred<{ id: number }>();
    mocks.updateSchedule
      .mockReturnValueOnce(saveA.promise)                        // A 慢保存
      .mockResolvedValueOnce({ id: 10 });                        // B 正常保存
    const onClose = vi.fn();
    const onSaved = vi.fn();

    const host = document.createElement('div');
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(
        <ScheduleEditorModal open editing={SCHEDULE_A} onClose={onClose} onSaved={onSaved} />,
      );
    });
    await flush(50);
    await click(saveButton()!);
    await flush(20);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledTimes(1);
    expect(saveButton()!.className).toContain('ant-btn-loading');

    // A 未结束时直接切换编辑对象到任务 B。
    await act(async () => {
      root.render(
        <ScheduleEditorModal open editing={SCHEDULE_B} onClose={onClose} onSaved={onSaved} />,
      );
    });
    await flush(50);
    expect(saveButton()!.className).not.toContain('ant-btn-loading');

    // B 立即可保存 —— 不需要等 A 的请求结束。
    await click(saveButton()!);
    await flush(20);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledTimes(2);
    expect(vi.mocked(updateSchedule)).toHaveBeenLastCalledWith(10, expect.anything());
    // B 自己的保存成功：onSaved/onClose 各一次（来自 B，而非 A）。
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);

    // A 最后才返回 —— 已经无法影响任何 UI。
    await act(async () => { saveA.resolve({ id: 9 }); });
    await flush(20);
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);

    await act(async () => { root.unmount(); });
    host.remove();
  });

  it('a stale session failure never toasts into the new editor', async () => {
    const saveA = deferred<{ id: number }>();
    mocks.updateSchedule.mockReturnValueOnce(saveA.promise);
    const onClose = vi.fn();
    const onSaved = vi.fn();

    const host = document.createElement('div');
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(
        <ScheduleEditorModal open editing={SCHEDULE_A} onClose={onClose} onSaved={onSaved} />,
      );
    });
    await flush(50);
    await click(saveButton()!);
    await flush(20);

    // 切到任务 B 后，A 的保存以失败告终 —— 旧会话的错误不属于新会话。
    await act(async () => {
      root.render(
        <ScheduleEditorModal open editing={SCHEDULE_B} onClose={onClose} onSaved={onSaved} />,
      );
    });
    await flush(50);
    await act(async () => { saveA.reject(new Error('A 的保存失败')); });
    await flush(20);
    expect(onSaved).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(saveButton()).toBeTruthy();
    expect(saveButton()!.className).not.toContain('ant-btn-loading');

    await act(async () => { root.unmount(); });
    host.remove();
  });
});
