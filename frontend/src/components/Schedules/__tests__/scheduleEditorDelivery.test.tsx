/**
 * useScheduleEditor / ScheduleEditorFields — 飞书投递目标与编辑器会话回归
 * （五次复审 P1-3 / P2-5 / P2-9）。
 *
 *   · 投递目标远程搜索：不再预拉「前 20 个联系人 + 前 100 个群聊」后本地
 *     过滤 —— 开启投递只拉群聊（user 空 query 不请求），输入姓名才走
 *     服务端搜索，第 21 个之后的员工也能选到；
 *   · 已选/回填目标注入 options：搜索词变化或回填目标不在候选页时，
 *     Select 不显示裸 id；
 *   · 投递目标加载失败 ≠ 没有目标（ERROR ≠ EMPTY）：全部失败 error +
 *     重试，部分失败 warning + 仍展示可用部分；
 *   · 智能体 Picker 快速关闭重开：第一屏请求必须 q=''，不闪旧词结果
 *     （不等 300ms 防抖）；
 *   · 保存 single-flight：同会话内连续触发 handleOk 两次，
 *     updateSchedule 只能被调用一次。
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
  fetchFeishuTargets: vi.fn(),
  updateSchedule: vi.fn(),
  createSchedule: vi.fn(),
  previewScheduleRuns: vi.fn(),
}));

vi.mock('@/services/runApi', () => ({
  fetchApplicationPage: mocks.fetchApplicationPage,
  resolveApplication: mocks.resolveApplication,
}));

vi.mock('@/services/shareApi', () => ({
  fetchFeishuTargets: mocks.fetchFeishuTargets,
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

import { useScheduleEditor } from '../useScheduleEditor';
import { ScheduleEditorFields } from '../ScheduleEditorFields';
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

/** antd 按钮两个中文字符会自动插空格 —— 统一压平后比较（同 session 测试）。 */
const btnText = (el: Element) => (el.textContent || '').replace(/\s+/g, '');

const findButton = (label: string) => (
  Array.from(document.querySelectorAll<HTMLButtonElement>('button'))
    .find((b) => btnText(b) === label));

/** Deferred promise — 由测试决定响应何时落地。 */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

type EditorState = ReturnType<typeof useScheduleEditor>;

/** 直接驱动 hook（single-flight 等场景需要拿到 handleOk 本体）。 */
let editorState: EditorState | null = null;

interface ProbeProps {
  open: boolean;
  editing: Schedule | null;
  presetApplicationId?: number;
  onClose?: () => void;
  onSaved?: () => void;
}

function EditorProbe(props: ProbeProps) {
  editorState = useScheduleEditor({
    open: props.open,
    editing: props.editing,
    presetApplicationId: props.presetApplicationId,
    onClose: props.onClose ?? (() => {}),
    onSaved: props.onSaved ?? (() => {}),
  });
  return <ScheduleEditorFields state={editorState} />;
}

const mounted: Array<{ host: HTMLElement; root: Root }> = [];
async function mountEditor(props: ProbeProps) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  mounted.push({ host, root });
  await act(async () => { root.render(<EditorProbe {...props} />); });
  await flush(30);
  return { root, host };
}

afterEach(async () => {
  while (mounted.length) {
    const { host, root } = mounted.pop()!;
    await act(async () => { root.unmount(); });
    host.remove();
  }
  document.body.innerHTML = '';
});
/**
 * 投递目标 Select —— 用 placeholder / 已选项定位（表单里还有智能体、时区
 * 等其它 Select）。
 */
function deliverySelect(): HTMLElement {
  const candidates = Array.from(document.querySelectorAll<HTMLElement>('.ant-select'));
  const byPlaceholder = candidates.find((s) => (
    s.querySelector('.ant-select-selection-placeholder')?.textContent
      === '搜索并选择飞书用户或群聊'
  ));
  if (byPlaceholder) return byPlaceholder;
  const bySelection = candidates.find((s) => (
    s.querySelector('.ant-select-selection-item')?.textContent?.includes('[群聊]')
  ));
  expect(bySelection, '未找到投递目标下拉').toBeTruthy();
  return bySelection!;
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
  mocks.fetchApplicationPage.mockReset().mockResolvedValue({
    items: [{ id: 7, name: '日报智能体', enabled: true, is_bound: true }],
    next_cursor: '', has_more: false,
  });
  mocks.resolveApplication.mockReset().mockResolvedValue({ id: 888, name: '日报智能体' });
  mocks.fetchFeishuTargets.mockReset().mockResolvedValue([]);
  mocks.updateSchedule.mockReset().mockResolvedValue({ id: 9 });
  mocks.createSchedule.mockReset().mockResolvedValue({ id: 1 });
  mocks.previewScheduleRuns.mockReset().mockResolvedValue([]);
  editorState = null;
});

describe('投递目标 — 远程搜索契约（五次复审 P1-3）', () => {
  it('enabling delivery loads CHATS only — an empty user query never fires', async () => {
    mocks.fetchFeishuTargets.mockResolvedValue([
      { id: 'oc-1', name: '运营群', avatar_url: '', target_type: 'chat' },
    ]);
    await mountEditor({ open: true, editing: null });

    // 投递未开启：完全不请求飞书目标。
    expect(mocks.fetchFeishuTargets).not.toHaveBeenCalled();

    const deliverySwitch = document.querySelector<HTMLButtonElement>('[role="switch"]');
    expect(deliverySwitch, '未找到投递开关').toBeTruthy();
    await click(deliverySwitch!);
    await flush(30);

    // 开启投递：拉群聊（空 query = 全量）；user 空 query 不请求 ——
    // 旧实现会拿「全公司任意前 20 人」再本地过滤。
    expect(mocks.fetchFeishuTargets).toHaveBeenCalledTimes(1);
    expect(mocks.fetchFeishuTargets).toHaveBeenLastCalledWith('chat', undefined);

    await click(deliverySelect().querySelector('.ant-select-selector')!);
    await flush(30);
    const option = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option'))
      .find((el) => el.textContent?.includes('运营群'));
    expect(option, '群聊应出现在候选里').toBeTruthy();
  });

  it('typing a name goes to the SERVER — the 21st coworker becomes findable', async () => {
    mocks.fetchFeishuTargets.mockImplementation(async (type: 'user' | 'chat', query?: string) => {
      if (type === 'user') {
        return query === '陈'
          ? [{ id: 'ou-21', name: '陈二十一半', avatar_url: '', target_type: 'user' }]
          : [];
      }
      return [{ id: 'oc-1', name: '运营群', avatar_url: '', target_type: 'chat' }];
    });
    await mountEditor({ open: true, editing: null });
    await click(document.querySelector<HTMLButtonElement>('[role="switch"]')!);
    await flush(30);

    await click(deliverySelect().querySelector('.ant-select-selector')!);
    await flush(30);
    const searchInput = deliverySelect()
      .querySelector<HTMLInputElement>('.ant-select-selection-search-input')!;
    await act(async () => { setNativeValue(searchInput, '陈'); });
    await flush(350); // 300ms 防抖

    // user 走服务端搜索（chat 也会用同一词过滤 —— 两个 hook 都发请求）。
    expect(mocks.fetchFeishuTargets).toHaveBeenCalledWith('user', '陈');
    const option = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option'))
      .find((el) => el.textContent?.includes('陈二十一半'));
    expect(option, '服务端搜索结果应出现在候选里').toBeTruthy();
  });

  it('editing backfills the bound target into options — never a bare id', async () => {
    // 回填的投递目标（oc-9 运营群）不在服务端返回里也必须显示名称。
    mocks.fetchFeishuTargets.mockResolvedValue([]);
    const withDelivery: Schedule = {
      ...editingSchedule,
      deliveries: [{
        id: 1,
        target_type: 'chat',
        target_id: 'oc-9',
        target_name: '回填运营群',
        content_mode: 'summary',
        enabled: true,
      }],
    };
    await mountEditor({ open: true, editing: withDelivery });
    await flush(30);

    // 开启投递（编辑回填自动开启）+ 群聊请求已发。
    expect(mocks.fetchFeishuTargets).toHaveBeenCalledWith('chat', undefined);
    const selection = deliverySelect()
      .querySelector('.ant-select-selection-item');
    expect(selection?.textContent).toContain('回填运营群');
  });

  it('a fully failed target load is an ERROR with retry — never a silent empty list', async () => {
    // 空 query 时只有 chat 在请求（user 空 query 不请求）→ 部分失败（warning）；
    // 输入搜索词后 user+chat 都在请求 → 全部失败必须是 error + 重试。
    mocks.fetchFeishuTargets.mockRejectedValue({
      response: { status: 502, data: { error: '获取群聊列表失败' } },
    });
    await mountEditor({ open: true, editing: null });
    await click(document.querySelector<HTMLButtonElement>('[role="switch"]')!);
    await flush(30);
    // 空查询：chat 失败、user 未请求 → 部分失败警告，不是静默空列表。
    expect(document.body.textContent).toContain('部分飞书目标加载失败');

    await click(deliverySelect().querySelector('.ant-select-selector')!);
    await flush(30);
    const searchInput = deliverySelect()
      .querySelector<HTMLInputElement>('.ant-select-selection-search-input')!;
    await act(async () => { setNativeValue(searchInput, '陈'); });
    await flush(350);
    // 有搜索词：user+chat 全部失败 → error。
    expect(document.body.textContent).toContain('加载飞书投递目标失败');

    const retry = findButton('重试');
    expect(retry, '重试入口').toBeTruthy();

    mocks.fetchFeishuTargets.mockResolvedValue([
      { id: 'oc-1', name: '恢复后的群', avatar_url: '', target_type: 'chat' },
    ]);
    await click(retry!);
    await flush(30);
    expect(document.body.textContent).not.toContain('加载飞书投递目标失败');
  });

  it('a PARTIAL failure keeps the working half and shows a warning', async () => {
    // 输入搜索词后：user 接口挂了，chat 正常 —— 仍展示群聊 + 部分失败警告。
    mocks.fetchFeishuTargets.mockImplementation(async (type: 'user' | 'chat') => {
      if (type === 'user') {
        throw { response: { status: 502, data: { error: '搜索联系人失败' } } };
      }
      return [{ id: 'oc-1', name: '运营群', avatar_url: '', target_type: 'chat' }];
    });
    await mountEditor({ open: true, editing: null });
    await click(document.querySelector<HTMLButtonElement>('[role="switch"]')!);
    await flush(30);

    await click(deliverySelect().querySelector('.ant-select-selector')!);
    await flush(30);
    const searchInput = deliverySelect()
      .querySelector<HTMLInputElement>('.ant-select-selection-search-input')!;
    await act(async () => { setNativeValue(searchInput, '运'); });
    await flush(350);

    expect(document.body.textContent).toContain('部分飞书目标加载失败');
    const option = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option'))
      .find((el) => el.textContent?.includes('运营群'));
    expect(option, '可用的一半（群聊）仍应展示').toBeTruthy();
  });
});

describe('编辑器会话 — Picker 快速关闭重开（五次复审 §36/§38）', () => {
  it('search → close → IMMEDIATELY reopen: first page carries no q and no old options flash', async () => {
    mocks.fetchApplicationPage.mockImplementation(async (opts: { q?: string }) => (
      opts?.q === '财务'
        ? { items: [{ id: 8, name: '财务助手', enabled: true, is_bound: true }], next_cursor: '', has_more: false }
        : { items: [{ id: 7, name: '日报智能体', enabled: true, is_bound: true }], next_cursor: '', has_more: false }
    ));
    const { root } = await mountEditor({ open: true, editing: null });

    // 搜索「财务」上服务端。
    const agentSelect = Array.from(document.querySelectorAll<HTMLElement>('.ant-select'))
      .find((s) => s.querySelector('#application_id'))!;
    await click(agentSelect.querySelector('.ant-select-selector')!);
    await flush(30);
    const searchInput = agentSelect
      .querySelector<HTMLInputElement>('.ant-select-selection-search-input')!;
    await act(async () => { setNativeValue(searchInput, '财务'); });
    await flush(400);
    expect(mocks.fetchApplicationPage).toHaveBeenLastCalledWith(
      expect.objectContaining({ q: '财务' }),
    );

    // 关闭编辑器（open=false），下一帧立即重开 —— 不等 300ms 防抖。
    mocks.fetchApplicationPage.mockClear();
    await act(async () => { root.render(<EditorProbe open={false} editing={null} />); });
    await flush(10);
    await act(async () => { root.render(<EditorProbe open editing={null} />); });
    await flush(30);

    // 重开后的第一屏请求不带旧词（空串 = 无搜索），且不闪旧查询结果。
    const calls = mocks.fetchApplicationPage.mock.calls;
    expect(calls.length).toBeGreaterThanOrEqual(1);
    for (const call of calls) {
      expect(call[0]?.q || '').toBe('');
    }
    expect(editorState!.apps.map((a) => a.name)).toEqual(['日报智能体']);
  });
});

describe('编辑器保存 — single-flight（五次复审 P2-9）', () => {
  it('triggering handleOk twice in the same session calls updateSchedule exactly ONCE', async () => {
    const save = deferred<{ id: number }>();
    mocks.updateSchedule.mockReturnValue(save.promise);
    await mountEditor({ open: true, editing: editingSchedule });
    await flush(30);

    // 第一次触发：请求在途。
    await act(async () => { void editorState!.handleOk(); });
    await flush(20);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledTimes(1);

    // 保存未结束时再次触发（组件层重复触发/double-submit）：被同步锁拦下。
    await act(async () => { await editorState!.handleOk(); });
    await flush(20);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledTimes(1);

    await act(async () => { save.resolve({ id: 9 }); });
    await flush(20);
    // 锁已释放：第三次触发可以再次保存。
    mocks.updateSchedule.mockResolvedValue({ id: 9 });
    await act(async () => { await editorState!.handleOk(); });
    await flush(20);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledTimes(2);
  });

  it('a NEW session is never blocked by the OLD session\'s in-flight save', async () => {
    // 会话换代（A → B）后，A 的保存仍在途：B 的保存必须立即可发 ——
    // 旧 operation 的锁不能把新会话的 synchronous lock 状态弄乱。
    const saveA = deferred<{ id: number }>();
    mocks.updateSchedule.mockReturnValueOnce(saveA.promise)
      .mockResolvedValueOnce({ id: 10 });
    const { root } = await mountEditor({ open: true, editing: editingSchedule });
    await flush(30);

    await act(async () => { void editorState!.handleOk(); });
    await flush(20);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledTimes(1);

    // 直接切换编辑对象（A → B，编辑器保持打开）。
    const scheduleB: Schedule = { ...editingSchedule, id: 10, name: '任务B' };
    await act(async () => { root.render(<EditorProbe open editing={scheduleB} />); });
    await flush(30);

    // B 的保存不被 A 的在途锁阻塞。
    await act(async () => { await editorState!.handleOk(); });
    await flush(20);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledTimes(2);
    expect(vi.mocked(updateSchedule)).toHaveBeenLastCalledWith(10, expect.anything());

    // A 的保存最后才返回：不影响任何 UI。
    await act(async () => { saveA.resolve({ id: 9 }); });
    await flush(20);
  });
});
