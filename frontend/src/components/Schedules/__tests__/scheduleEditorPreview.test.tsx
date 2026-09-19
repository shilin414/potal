/**
 * useScheduleEditor — 执行预览代际回归（四次复审 P2-5）。
 *
 * 预览请求在途时用户仍可修改执行时间/星期/时区。旧表单算出的预览晚到后
 * 不得覆盖新配置下的展示 —— 表单触发字段一变：旧预览立即作废（结果清空、
 * spinner 归还），晚到的旧响应由 seq 守卫丢弃，新预览按需重新计算。
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
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
}));

import { ScheduleEditorModal } from '../ScheduleEditorModal';

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

/** Deferred promise — 由测试决定响应何时落地。 */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
}

const editingSchedule: Schedule = {
  id: 9,
  name: '每日日报',
  description: '',
  application_id: 888,
  prompt: '总结今天的数据',
  schedule_type: 'daily',
  cron_expression: '0 0 9 * * *',
  timezone: 'Asia/Shanghai',
  run_at: null,
  trigger: { time: '09:00', days_of_week: [1, 2, 3, 4, 5], day_of_month: 1 },
  enabled: true,
  conversation_policy: 'new_each_run',
  overlap_policy: 'queue',
  misfire_policy: 'fire_once',
  deadline_policy: 'execute_anyway',
  execution_window_seconds: 0,
  next_run_at: null,
  last_run_at: null,
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z',
  deliveries: [],
};

/** 预览按钮（原生 button.schedule-preview-btn）。 */
const previewButton = () => document.querySelector<HTMLButtonElement>(
  'button.schedule-preview-btn')!;

/** 预览列表项。 */
const previewItems = () => Array.from(document.querySelectorAll('.schedule-preview-list li'));

/** 重复方式 Radio（原生 label.ant-radio-button-wrapper，比下拉更稳定）。 */
const scheduleTypeRadio = (label: string) => (
  Array.from(document.querySelectorAll<HTMLElement>('.ant-radio-button-wrapper'))
    .find((el) => el.textContent === label)!
);

const mounted: Array<{ host: HTMLElement; root: Root }> = [];

async function mountEditor() {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  mounted.push({ host, root });
  await act(async () => {
    root.render(
      <ScheduleEditorModal open editing={editingSchedule} onClose={() => {}} onSaved={() => {}} />,
    );
  });
  await flush(50);
}

afterEach(async () => {
  while (mounted.length) {
    const { host, root } = mounted.pop()!;
    await act(async () => { root.unmount(); });
    host.remove();
  }
  document.body.innerHTML = '';
});

beforeEach(() => {
  mocks.fetchApplicationPage.mockReset().mockResolvedValue({
    // 目标智能体就在第一页：无 resolve，配置域安静。
    items: [{ id: 888, name: '日报智能体', enabled: true, is_bound: true }],
    next_cursor: '', has_more: false,
  });
  mocks.resolveApplication.mockReset();
  mocks.updateSchedule.mockReset();
  mocks.createSchedule.mockReset();
  mocks.previewScheduleRuns.mockReset();
});

describe('useScheduleEditor — 执行预览代际（四次复审 P2-5）', () => {
  it('a trigger change mid-flight drops the stale preview; the new one renders on demand', async () => {
    const previewA = deferred<string[]>();
    mocks.previewScheduleRuns.mockReturnValueOnce(previewA.promise);

    await mountEditor();

    // 点预览 → 请求 A（按「每天」计算）在途，按钮进入计算中。
    await click(previewButton());
    await flush(20);
    expect(previewButton().textContent).toContain('计算中');

    // 用户把重复方式改成「每周」—— 旧预览当场作废：spinner 归还、结果清空。
    await click(scheduleTypeRadio('每周'));
    expect(previewButton().textContent).not.toContain('计算中');

    // 旧预览此刻才返回（对应「每天」的结果）—— 不得上屏。
    await act(async () => {
      previewA.resolve(['2026-10-01T09:00:00+08:00', '2026-10-02T09:00:00+08:00']);
    });
    await flush(20);
    expect(previewItems()).toHaveLength(0);

    // 重新点预览 → 新配置（每周）的结果正常上屏。
    mocks.previewScheduleRuns.mockResolvedValueOnce(['2026-10-05T09:00:00+08:00']);
    await click(previewButton());
    await flush(20);
    expect(previewItems()).toHaveLength(1);
  });

  it('a rendered preview is cleared the moment a trigger field changes', async () => {
    mocks.previewScheduleRuns.mockResolvedValueOnce([
      '2026-10-01T09:00:00+08:00',
      '2026-10-02T09:00:00+08:00',
    ]);

    await mountEditor();

    await click(previewButton());
    await flush(20);
    expect(previewItems()).toHaveLength(2);

    // 改重复方式 → 已上屏的旧预览立即消失（不再是当前配置的结果）。
    await click(scheduleTypeRadio('单次'));
    await flush(20);
    expect(previewItems()).toHaveLength(0);
  });
});
