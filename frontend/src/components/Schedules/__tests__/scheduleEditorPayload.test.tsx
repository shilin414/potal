/**
 * ScheduleEditorModal 提交报文回归测试。
 *
 * 回归背景（P0）：`handleOk` 曾经用 `await form.validateFields()` 的返回值拼 payload。
 * rc-field-form 的 `validateFields()` / `getFieldsValue()`（不带 true）只返回**已注册
 * Form.Item 的路径**（用注册路径重建对象），于是通过 `setFieldsValue` 写入、但没有
 * 对应 Form.Item 的字段会被静默丢弃：
 *   - 飞书投递只注册了 `deliveries[0].target_id` → `target_type` / `target_name` 消失，
 *     后端以 `delivery target_type must be user or chat` 拒绝创建/更新；
 *   - `misfire_policy` / `deadline_policy` / `execution_window_seconds` 没有 Form.Item
 *     → 同样消失（此前只是靠后端默认值蒙对）。
 *
 * 因此本测试断言提交报文本身：创建（POST）与编辑（PATCH）两条路径都必须带上完整的
 * 投递三元组。
 */
// @vitest-environment jsdom
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

import type { Schedule, ScheduleUpsertPayload } from '@/types/schedule';

const sent: ScheduleUpsertPayload[] = [];

const lastPayload = (): ScheduleUpsertPayload => sent[sent.length - 1];

// The picker reads the PAGED endpoint (执行报告 §16.2) — the legacy
// whole-array client no longer exists, so the mock has to answer the paged
// shape the component actually consumes. resolveApplication backs the
// out-of-page backfill (P1-2/P1-3): id 7 is on the mocked page so it stays
// unused, but the export must exist — useScheduleEditor imports it.
vi.mock('@/services/runApi', () => ({
  fetchApplicationPage: vi.fn(async () => ({
    items: [{ id: 7, name: '日报智能体', enabled: true, is_bound: true }],
    next_cursor: '',
    has_more: false,
  })),
  resolveApplication: vi.fn(async () => ({ name: '日报智能体' })),
}));

vi.mock('@/services/shareApi', () => ({
  fetchFeishuTargets: vi.fn(async (type: 'user' | 'chat') =>
    type === 'user'
      ? [{ id: 'ou_1', name: '张三', avatar_url: '', target_type: 'user' }]
      : [{ id: 'oc_1', name: '运营群', avatar_url: '', target_type: 'chat' }],
  ),
}));

vi.mock('@/services/scheduleApi', () => ({
  createSchedule: vi.fn(async (p: ScheduleUpsertPayload) => {
    sent.push(p);
    return { id: 1 };
  }),
  updateSchedule: vi.fn(async (_id: number, p: ScheduleUpsertPayload) => {
    sent.push(p);
    return { id: 1 };
  }),
  previewScheduleRuns: vi.fn(async () => [] as string[]),
}));

import { ScheduleEditorModal } from '../ScheduleEditorModal';

// ── jsdom 环境补齐（antd 依赖） ────────────────────────────────────
(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};
(globalThis as unknown as { matchMedia: unknown }).matchMedia = (query: string) => ({
  matches: false,
  media: query,
  onchange: null,
  addListener() {},
  removeListener() {},
  addEventListener() {},
  removeEventListener() {},
  dispatchEvent: () => false,
});

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

/** React 受控 input 需要走原生 setter 才会触发 onChange。 */
function setNativeValue(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement
    ? HTMLTextAreaElement.prototype
    : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, 'value')!.set!.call(el, value);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}

function click(el: Element) {
  return act(async () => {
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

async function pickOption(placeholder: string, optionText: string) {
  const select = Array.from(document.querySelectorAll<HTMLElement>('.ant-select'))
    .find((el) => el.textContent?.includes(placeholder));
  expect(select, `未找到 placeholder 为 ${placeholder} 的下拉`).toBeTruthy();
  await click(select!.querySelector('.ant-select-selector')!);
  await flush(30);

  const option = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option'))
    .find((el) => el.textContent?.includes(optionText));
  expect(option, `未找到候选 ${optionText}`).toBeTruthy();
  await click(option!);
  await flush(30);
}

async function clickFooter(label: string) {
  const btn = Array.from(document.querySelectorAll<HTMLElement>('.ant-modal-footer .ant-btn'))
    .find((el) => (el.textContent || '').replace(/\s+/g, '') === label);
  expect(btn, `未找到底部按钮 ${label}`).toBeTruthy();
  await click(btn!);
  await flush(60);
}

async function mount(node: React.ReactElement): Promise<{ host: HTMLElement; root: Root }> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(node);
  });
  await flush(30);
  return { host, root };
}

const editingSchedule: Schedule = {
  id: 9,
  name: '每日日报',
  description: '',
  application_id: 7,
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
  deliveries: [{
    id: 1,
    target_type: 'chat',
    target_id: 'oc_1',
    target_name: '运营群',
    content_mode: 'summary',
    enabled: true,
  }],
};

const fullDelivery = {
  target_type: 'chat',
  target_id: 'oc_1',
  target_name: '运营群',
  content_mode: 'summary',
};

describe('ScheduleEditorModal 提交报文', () => {
  it('创建：deliveries[0] 必须带 target_type / target_name', async () => {
    sent.length = 0;
    const { root } = await mount(
      <ScheduleEditorModal open editing={null} presetApplicationId={7} onClose={() => {}} onSaved={() => {}} />,
    );

    await click(document.querySelector<HTMLElement>('.ant-modal .ant-switch')!);
    await flush(30);
    await pickOption('选择飞书用户或群聊', '运营群');
    await act(async () => {
      setNativeValue(document.querySelector<HTMLInputElement>('#name')!, '每日日报');
      setNativeValue(document.querySelector<HTMLTextAreaElement>('#prompt')!, '总结今天的数据');
    });
    await flush(10);
    await clickFooter('创建');

    expect(lastPayload().deliveries?.[0]).toEqual(fullDelivery);
    await act(async () => {
      root.unmount();
    });
  });

  it('编辑：不重新选择目标也要保留完整投递信息', async () => {
    sent.length = 0;
    const { root } = await mount(
      <ScheduleEditorModal open editing={editingSchedule} onClose={() => {}} onSaved={() => {}} />,
    );

    await act(async () => {
      setNativeValue(document.querySelector<HTMLInputElement>('#name')!, '每日日报（改）');
    });
    await flush(10);
    await clickFooter('保存');

    expect(lastPayload().deliveries?.[0]).toEqual(fullDelivery);
    await act(async () => {
      root.unmount();
    });
  });
});
