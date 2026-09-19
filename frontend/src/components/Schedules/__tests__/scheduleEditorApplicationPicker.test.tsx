/**
 * ScheduleEditorFields / useScheduleEditor — agent picker data boundary
 * (二次复审 P1-2/P1-3).
 *
 * The selector used to download ONE page of 100 agents and filter it in the
 * browser — agents 101+ could never be scheduled. Pin the new contract:
 *   · the search term goes to the SERVER (q on fetchApplicationPage), so an
 *     agent outside the first page is findable;
 *   · has_more + onPopupScroll loads the next cursor page;
 *   · editing a schedule (or a preset agent id) whose application is NOT on
 *     the first page resolves it through the CONSUMER resolver
 *     resolveApplication({ id }) — never the staff-only authoring read
 *     fetchApplicationDetail — so the Select never shows blank.
 */
// @vitest-environment jsdom
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

import type { Schedule } from '@/types/schedule';

const mocks = vi.hoisted(() => ({
  fetchApplicationPage: vi.fn(),
  resolveApplication: vi.fn(),
  fetchApplicationDetail: vi.fn(),
}));

vi.mock('@/services/runApi', () => ({
  fetchApplicationPage: mocks.fetchApplicationPage,
  resolveApplication: mocks.resolveApplication,
  fetchApplicationDetail: mocks.fetchApplicationDetail,
}));

vi.mock('@/services/shareApi', () => ({
  fetchFeishuTargets: vi.fn(async () => ({ items: [], next_cursor: '', has_more: false })),
}));

vi.mock('@/services/scheduleApi', () => ({
  createSchedule: vi.fn(async () => ({ id: 1 })),
  updateSchedule: vi.fn(async () => ({ id: 1 })),
  previewScheduleRuns: vi.fn(async () => []),
}));

import { ScheduleEditorModal } from '../ScheduleEditorModal';
import { updateSchedule } from '@/services/scheduleApi';

// ── jsdom 环境补齐（antd 依赖） ────────────────────────────────────
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

function setNativeValue(el: HTMLInputElement, value: string) {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!
    .set!.call(el, value);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}

function click(el: Element) {
  return act(async () => {
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

async function mount(node: React.ReactElement): Promise<{ host: HTMLElement; root: Root }> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(node);
  });
  await flush(50);
  return { host, root };
}

async function unmount(root: Root, host: HTMLElement) {
  await act(async () => { root.unmount(); });
  host.remove();
  document.body.innerHTML = '';
}

/** The 智能体 Select — Form.Item stamps its name onto the search input id. */
function agentSelect() {
  const input = document.querySelector('#application_id');
  expect(input, '未找到智能体下拉').toBeTruthy();
  return input!.closest('.ant-select')!;
}

/** The selected value INSIDE the 智能体 Select (other Selects exist on the form). */
function agentSelectionItem() {
  return agentSelect().querySelector('.ant-select-selection-item');
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

beforeEach(() => {
  mocks.fetchApplicationPage.mockReset();
  mocks.resolveApplication.mockReset();
  mocks.fetchApplicationDetail.mockReset();
});

describe('agent picker — server-side search (P1-2)', () => {
  it('sends the search term to the server and lists an agent outside page one', async () => {
    mocks.fetchApplicationPage.mockImplementation(async (opts: { q?: string }) => (
      opts?.q === '财务'
        ? { items: [{ id: 8, name: '财务助手', enabled: true, is_bound: true }], next_cursor: '', has_more: false }
        : { items: [{ id: 7, name: '日报智能体', enabled: true, is_bound: true }], next_cursor: '', has_more: false }
    ));

    const { root, host } = await mount(
      <ScheduleEditorModal open editing={null} onClose={() => {}} onSaved={() => {}} />,
    );

    await click(agentSelect().querySelector('.ant-select-selector')!);
    await flush(30);
    expect(mocks.fetchApplicationPage).toHaveBeenLastCalledWith(
      expect.not.objectContaining({ q: '财务' }),
    );

    const searchInput = agentSelect()
      .querySelector<HTMLInputElement>('.ant-select-selection-search-input')!;
    await act(async () => { setNativeValue(searchInput, '财务'); });
    await flush(400); // useApplicationPage's 300 ms debounce

    expect(mocks.fetchApplicationPage).toHaveBeenLastCalledWith(
      expect.objectContaining({ q: '财务' }),
    );
    const option = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option'))
      .find((el) => el.textContent?.includes('财务助手'));
    expect(option, '服务端搜索结果应出现在候选里').toBeTruthy();

    await unmount(root, host);
  });
});

describe('agent picker — popup scroll pagination (P1-2)', () => {
  it('scrolling near the bottom of the dropdown fetches the next cursor page', async () => {
    // Small pages: antd's virtual list renders only what fits the (jsdom
    // zero-height) viewport, so keep the total option count tiny.
    mocks.fetchApplicationPage.mockImplementation(async (opts: { cursor?: string }) => (
      opts?.cursor === 'c1'
        ? { items: [{ id: 60, name: '第二页智能体', enabled: true, is_bound: true }], next_cursor: '', has_more: false }
        : {
          items: [
            { id: 1, name: '智能体1', enabled: true, is_bound: true },
            { id: 2, name: '智能体2', enabled: true, is_bound: true },
            { id: 3, name: '智能体3', enabled: true, is_bound: true },
          ],
          next_cursor: 'c1', has_more: true,
        }
    ));

    const { root, host } = await mount(
      <ScheduleEditorModal open editing={null} onClose={() => {}} onSaved={() => {}} />,
    );

    await click(agentSelect().querySelector('.ant-select-selector')!);
    await flush(30);
    expect(mocks.fetchApplicationPage).toHaveBeenCalledTimes(1);

    // jsdom: scrollHeight/clientHeight are both 0 → the near-bottom check
    // (scrollHeight - scrollTop - clientHeight < 24) fires on any scroll event.
    // rc-select binds onPopupScroll to the virtual-list holder, not to the
    // dropdown root.
    const holder = document.querySelector('.rc-virtual-list-holder');
    expect(holder, '下拉未打开').toBeTruthy();
    await act(async () => {
      holder!.dispatchEvent(new Event('scroll'));
    });
    await flush(30);

    expect(mocks.fetchApplicationPage).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'c1' }),
    );
    const option = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option'))
      .find((el) => el.textContent?.includes('第二页智能体'));
    expect(option, '第二页候选应出现在下拉里').toBeTruthy();

    await unmount(root, host);
  });
});

describe('agent picker — editing backfill (P1-2/P1-3)', () => {
  it('resolves the bound agent through the CONSUMER resolver, never the authoring detail read', async () => {
    mocks.fetchApplicationPage.mockResolvedValue({
      items: [{ id: 7, name: '日报智能体', enabled: true, is_bound: true }],
      next_cursor: '', has_more: false,
    });
    mocks.resolveApplication.mockResolvedValue({ name: '第一页之外的智能体' });

    const { root, host } = await mount(
      <ScheduleEditorModal open editing={editingSchedule} onClose={() => {}} onSaved={() => {}} />,
    );

    expect(mocks.resolveApplication).toHaveBeenCalledWith({ id: 888 });
    expect(mocks.fetchApplicationDetail).not.toHaveBeenCalled();
    // The Select shows the resolved name instead of a blank value.
    const selected = agentSelectionItem();
    expect(selected?.textContent).toContain('第一页之外的智能体');

    await unmount(root, host);
  });

/** antd 按钮文本可能带换行/空格 —— 统一压平后比较（同 payload 测试）。 */
const btnText = (el: Element) => (el.textContent || '').replace(/\s+/g, '');

const findButton = (label: string) => (
  Array.from(document.querySelectorAll<HTMLButtonElement>('button'))
    .find((b) => btnText(b) === label));

  it('a 404 resolve is UNAVAILABLE: placeholder name, explicit warning, save blocked (§41)', async () => {
    mocks.fetchApplicationPage.mockResolvedValue({
      items: [{ id: 7, name: '日报智能体', enabled: true, is_bound: true }], next_cursor: '', has_more: false,
    });
    mocks.resolveApplication.mockRejectedValue({ response: { status: 404 } });

    const { root, host } = await mount(
      <ScheduleEditorModal open editing={editingSchedule} onClose={() => {}} onSaved={() => {}} />,
    );
    await flush(50);

    // The Select still shows a non-blank placeholder…
    expect(agentSelectionItem()?.textContent).toContain('智能体 #888');
    // …an explicit warning says the agent can no longer run…
    expect(document.body.textContent).toContain('原智能体当前不可用');
    // …and 保存 is refused until a new agent is picked.
    const save = Array.from(document.querySelectorAll<HTMLButtonElement>(
      '.ant-modal-footer .ant-btn')).find((b) => btnText(b) === '保存');
    expect(save).toBeTruthy();
    vi.mocked(updateSchedule).mockClear();
    await click(save!);
    await flush(50);
    expect(vi.mocked(updateSchedule)).not.toHaveBeenCalled();

    await unmount(root, host);
  });

  it('picking a REPLACEMENT agent hides the unavailable warning immediately (四次复审 P2-4)', async () => {
    mocks.fetchApplicationPage.mockResolvedValue({
      items: [{ id: 7, name: '日报智能体', enabled: true, is_bound: true }], next_cursor: '', has_more: false,
    });
    mocks.resolveApplication.mockRejectedValue({ response: { status: 404 } });

    const { root, host } = await mount(
      <ScheduleEditorModal open editing={editingSchedule} onClose={() => {}} onSaved={() => {}} />,
    );
    await flush(50);
    // 原 Agent 404 → unavailable 告警在屏。
    expect(document.body.textContent).toContain('原智能体当前不可用');

    // 用户已经在 Select 里换成了新智能体 → 告警必须立即消失。告警绑定
    // 「当前字段值」，而不是 resolve 时的目标（旧状态不得跟随新选择）。
    await click(agentSelect().querySelector('.ant-select-selector')!);
    await flush(30);
    const option = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option'))
      .find((el) => el.textContent?.includes('日报智能体'));
    expect(option, '替换候选应出现在下拉里').toBeTruthy();
    await click(option!);
    await flush(30);
    expect(document.body.textContent).not.toContain('原智能体当前不可用');
    expect(agentSelectionItem()?.textContent).toContain('日报智能体');

    // 换了智能体后保存不再被 unavailable 拦截。
    vi.mocked(updateSchedule).mockClear();
    const save = Array.from(document.querySelectorAll<HTMLButtonElement>(
      '.ant-modal-footer .ant-btn')).find((b) => btnText(b) === '保存');
    await click(save!);
    await flush(50);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledTimes(1);

    await unmount(root, host);
  });

  it('a 5xx / network resolve failure is RETRYABLE — never faked as 智能体 #id (§42)', async () => {
    mocks.fetchApplicationPage.mockResolvedValue({
      items: [{ id: 7, name: '日报智能体', enabled: true, is_bound: true }],
      next_cursor: '', has_more: false,
    });
    mocks.resolveApplication
      .mockRejectedValueOnce(new Error('network down'))
      .mockResolvedValueOnce({ name: '恢复后的智能体' });

    const { root, host } = await mount(
      <ScheduleEditorModal open editing={editingSchedule} onClose={() => {}} onSaved={() => {}} />,
    );
    await flush(50);

    // No fake 智能体 #id for a transient failure — an explicit error + retry.
    expect(agentSelectionItem()?.textContent ?? '').not.toContain('智能体 #888');
    expect(document.body.textContent).toContain('无法加载智能体信息');
    const retry = findButton('重试');
    expect(retry).toBeTruthy();

    await click(retry!);
    await flush(50);
    expect(mocks.resolveApplication).toHaveBeenCalledTimes(2);
    expect(agentSelectionItem()?.textContent).toContain('恢复后的智能体');
    expect(document.body.textContent).not.toContain('无法加载智能体信息');

    await unmount(root, host);
  });

  it('a failed agent LIST surfaces an error + retry — never an empty-looking Select (§38–§39)', async () => {
    mocks.fetchApplicationPage
      .mockRejectedValueOnce(new Error('目录服务故障'))
      .mockResolvedValueOnce({
        items: [{ id: 7, name: '恢复后的智能体', enabled: true, is_bound: true }],
        next_cursor: '', has_more: false,
      });

    const { root, host } = await mount(
      <ScheduleEditorModal open editing={null} onClose={() => {}} onSaved={() => {}} />,
    );
    await flush(50);

    expect(document.body.textContent).toContain('加载智能体失败');
    const retry = findButton('重试');
    expect(retry).toBeTruthy();

    await click(retry!);
    await flush(50);
    expect(mocks.fetchApplicationPage).toHaveBeenCalledTimes(2);
    expect(document.body.textContent).not.toContain('加载智能体失败');

    await unmount(root, host);
  });

  it('a preset agent id from a card/chat page resolves the same way (P1-3)', async () => {
    mocks.fetchApplicationPage.mockResolvedValue({
      items: [{ id: 7, name: '日报智能体', enabled: true, is_bound: true }],
      next_cursor: '', has_more: false,
    });
    mocks.resolveApplication.mockResolvedValue({ name: '卡片带入的智能体' });

    const { root, host } = await mount(
      <ScheduleEditorModal open editing={null} presetApplicationId={888} onClose={() => {}} onSaved={() => {}} />,
    );

    expect(mocks.resolveApplication).toHaveBeenCalledWith({ id: 888 });
    const selected = agentSelectionItem();
    expect(selected?.textContent).toContain('卡片带入的智能体');

    await unmount(root, host);
  });

  it('a target that IS on the first page sends no resolve request', async () => {
    mocks.fetchApplicationPage.mockResolvedValue({
      items: [{ id: 7, name: '日报智能体', enabled: true, is_bound: true }],
      next_cursor: '', has_more: false,
    });

    const { root, host } = await mount(
      <ScheduleEditorModal
        open
        editing={{ ...editingSchedule, application_id: 7 }}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );
    await flush(50);

    expect(mocks.resolveApplication).not.toHaveBeenCalled();
    expect(mocks.fetchApplicationDetail).not.toHaveBeenCalled();
    const selected = agentSelectionItem();
    expect(selected?.textContent).toContain('日报智能体');

    await unmount(root, host);
  });
});
