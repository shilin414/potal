/**
 * useScheduleEditor / ScheduleEditorFields — 飞书投递目标与编辑器会话回归
 * （五次复审 P1-3 / P2-5 / P2-9，六次复审 P1-1 / P1-3 / P2-3）。
 *
 *   · 投递目标远程搜索：不再预拉「前 20 个联系人 + 前 100 个群聊」后本地
 *     过滤 —— 开启投递只拉群聊（user 空 query 不请求），输入姓名才走
 *     服务端搜索，第 21 个之后的员工也能选到；
 *   · 联系人 cursor 分页（六次复审 P1-3）：滚到底续拉下一页，第 51+ 个
 *     匹配不再被第一页截断；
 *   · chat 会话缓存（六次复审 P2-1）：输入搜索词不再触发后端全量群聊
 *     扫描 —— 一个编辑会话只拉一次，过滤在本地完整数据集上完成；
 *   · 关闭投递必须显式 PATCH deliveries: []（六次复审 P1-1）：后端契约
 *     是「deliveries 缺失 = 保留原有投递」，undefined 会让旧投递静默
 *     存活 —— 用户已关闭通知，任务执行后仍继续发飞书；
 *   · idle ≠ failed（六次复审 P2-3）：空 query 下 user 是 idle（未参与
 *     查询），chat 失败 = 全部失败（不再是「部分失败」）；
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
import type { FeishuForwardTarget, FeishuForwardTargetPage } from '@/services/shareApi';

/** 官方分页 envelope（六次复审 P1-3）：{items, next_cursor, has_more}。 */
const page = (
  items: FeishuForwardTarget[],
  opts?: { hasMore?: boolean; cursor?: string },
): FeishuForwardTargetPage => ({
  items,
  next_cursor: opts?.cursor ?? '',
  has_more: opts?.hasMore ?? false,
});

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
  mocks.fetchFeishuTargets.mockReset().mockResolvedValue(page([]));
  mocks.updateSchedule.mockReset().mockResolvedValue({ id: 9 });
  mocks.createSchedule.mockReset().mockResolvedValue({ id: 1 });
  mocks.previewScheduleRuns.mockReset().mockResolvedValue([]);
  editorState = null;
});

describe('投递目标 — 远程搜索契约（五次复审 P1-3）', () => {
  it('enabling delivery loads CHATS only — an empty user query never fires', async () => {
    mocks.fetchFeishuTargets.mockResolvedValue(page([
      { id: 'oc-1', name: '运营群', avatar_url: '', target_type: 'chat' },
    ]));
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

  it('typing a name goes to the SERVER — and does NOT re-scan the full chat list', async () => {
    // 六次复审 P2-1：chat 是会话内缓存 + 本地过滤 —— 输入「陈」只触发
    // user 的服务端搜索，chat 不得再次全量请求（旧实现每个字符都重扫）。
    mocks.fetchFeishuTargets.mockImplementation(
      async (type: 'user' | 'chat', query?: string, cursor?: string) => {
        if (type === 'user') {
          return cursor === 'p2'
            ? page([{ id: 'ou-52', name: '陈五十二', avatar_url: '', target_type: 'user' }])
            : page(
              [{ id: 'ou-21', name: '陈二十一半', avatar_url: '', target_type: 'user' }],
              { hasMore: true, cursor: 'p2' },
            );
        }
        expect(query, 'chat 请求不得携带搜索词（本地过滤）').toBeUndefined();
        return page([{ id: 'oc-1', name: '运营群', avatar_url: '', target_type: 'chat' }]);
      },
    );
    await mountEditor({ open: true, editing: null });
    await click(document.querySelector<HTMLButtonElement>('[role="switch"]')!);
    await flush(30);
    expect(mocks.fetchFeishuTargets).toHaveBeenCalledTimes(1); // chat 全量

    await click(deliverySelect().querySelector('.ant-select-selector')!);
    await flush(30);
    const searchInput = deliverySelect()
      .querySelector<HTMLInputElement>('.ant-select-selection-search-input')!;
    await act(async () => { setNativeValue(searchInput, '陈'); });
    await flush(350); // 300ms 防抖

    // user 走服务端搜索；chat 保持会话缓存（仍只有那 1 次请求）。
    expect(mocks.fetchFeishuTargets).toHaveBeenCalledTimes(2);
    expect(mocks.fetchFeishuTargets).toHaveBeenLastCalledWith('user', '陈');
    const option = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option'))
      .find((el) => el.textContent?.includes('陈二十一半'));
    expect(option, '服务端搜索结果应出现在候选里').toBeTruthy();

    // 联系人 cursor 分页（六次复审 P1-3）：滚到底续拉，第 51+ 人可见。
    await act(async () => { await editorState!.loadMoreTargets(); });
    await flush(20);
    expect(mocks.fetchFeishuTargets).toHaveBeenLastCalledWith('user', '陈', 'p2');
    expect(editorState!.targets.some((t) => t.name === '陈五十二')).toBe(true);
  });

  it('editing backfills the bound target into options — never a bare id', async () => {
    // 回填的投递目标（oc-9 运营群）不在服务端返回里也必须显示名称。
    mocks.fetchFeishuTargets.mockResolvedValue(page([]));
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

  it('an EMPTY-query chat failure is a FULL failure — idle user did not participate', async () => {
    // 六次复审 P2-3：空 query 时只有 chat 参与查询（user 是 idle）——
    // chat 失败 = 一个目标都没有加载成功，必须是 error + 重试，而不是
    // 旧判定的「部分失败」（页面曾提示「仅显示可用部分」）。
    mocks.fetchFeishuTargets.mockRejectedValue({
      response: { status: 502, data: { error: '获取群聊列表失败' } },
    });
    await mountEditor({ open: true, editing: null });
    await click(document.querySelector<HTMLButtonElement>('[role="switch"]')!);
    await flush(30);
    expect(document.body.textContent).toContain('加载飞书投递目标失败');
    expect(document.body.textContent).not.toContain('部分飞书目标加载失败');

    const retry = findButton('重试');
    expect(retry, '重试入口').toBeTruthy();

    mocks.fetchFeishuTargets.mockResolvedValue(page([
      { id: 'oc-1', name: '恢复后的群', avatar_url: '', target_type: 'chat' },
    ]));
    await click(retry!);
    await flush(30);
    expect(document.body.textContent).not.toContain('加载飞书投递目标失败');
  });

  it('with a query, both sources engaged and all failing is an ERROR', async () => {
    // 有搜索词：user + chat 都参与 —— 全部失败必须是 error + 重试。
    mocks.fetchFeishuTargets.mockRejectedValue({
      response: { status: 502, data: { error: '获取群聊列表失败' } },
    });
    await mountEditor({ open: true, editing: null });
    await click(document.querySelector<HTMLButtonElement>('[role="switch"]')!);
    await flush(30);

    await click(deliverySelect().querySelector('.ant-select-selector')!);
    await flush(30);
    const searchInput = deliverySelect()
      .querySelector<HTMLInputElement>('.ant-select-selection-search-input')!;
    await act(async () => { setNativeValue(searchInput, '陈'); });
    await flush(350);
    expect(document.body.textContent).toContain('加载飞书投递目标失败');
  });

  it('a PARTIAL failure keeps the working half and shows a warning', async () => {
    // 输入搜索词后：user 接口挂了，chat 已缓存成功 —— 仍展示群聊 + 部分
    // 失败警告（engaged = user(error) + chat(success)）。
    mocks.fetchFeishuTargets.mockImplementation(async (type: 'user' | 'chat') => {
      if (type === 'user') {
        throw { response: { status: 502, data: { error: '搜索联系人失败' } } };
      }
      return page([{ id: 'oc-1', name: '运营群', avatar_url: '', target_type: 'chat' }]);
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

  it('a user PAGE-2 failure is a paging error, not a source failure (七次复审 P1-2)', async () => {
    // user page1 成功（hasMore）、page2 失败、chat 成功：已加载的候选仍然
    // 可选，只出现「更多联系人加载失败」，绝不出现「加载飞书投递目标
    // 失败」（那是首页全部失败才有的整屏错误）。
    mocks.fetchFeishuTargets.mockImplementation(
      async (type: 'user' | 'chat', query?: string, cursor?: string) => {
        if (type === 'chat') {
          expect(query, 'chat 请求不得携带搜索词（本地过滤）').toBeUndefined();
          return page([{ id: 'oc-1', name: '运营群', avatar_url: '', target_type: 'chat' }]);
        }
        if (cursor === 'p2') {
          throw { response: { status: 502, data: { error: '搜索联系人失败' } } };
        }
        return page(
          [{ id: 'ou-21', name: '陈二十一半', avatar_url: '', target_type: 'user' }],
          { hasMore: true, cursor: 'p2' },
        );
      },
    );
    await mountEditor({ open: true, editing: null });
    await click(document.querySelector<HTMLButtonElement>('[role="switch"]')!);
    await flush(30);

    await click(deliverySelect().querySelector('.ant-select-selector')!);
    await flush(30);
    const searchInput = deliverySelect()
      .querySelector<HTMLInputElement>('.ant-select-selection-search-input')!;
    await act(async () => { setNativeValue(searchInput, '陈'); });
    await flush(350);
    const option = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option'))
      .find((el) => el.textContent?.includes('陈二十一半'));
    expect(option, '第一页的联系人应出现在候选里').toBeTruthy();

    // page2 拉挂。
    await act(async () => { await editorState!.loadMoreTargets(); });
    await flush(20);

    expect(document.body.textContent).toContain('更多联系人加载失败');
    expect(document.body.textContent).not.toContain('加载飞书投递目标失败');
    expect(document.body.textContent).not.toContain('部分飞书目标加载失败');
    // 已加载的候选不受影响：首页的联系人仍在 options 里（chat 候选按当前
    // 搜索词「陈」本地过滤，运营群不匹配属正常 —— 关键是它没有变成错误态）。
    expect(editorState!.targets.some((t) => t.name === '陈二十一半')).toBe(true);

    // 重试入口从断点续拉（loadMore，不是 refresh 回第一页）。
    const retry = findButton('重试');
    expect(retry, '分页失败的重试入口').toBeTruthy();
    mocks.fetchFeishuTargets.mockImplementation(
      async (type: 'user' | 'chat', query?: string, cursor?: string) => {
        if (type === 'chat') {
          return page([{ id: 'oc-1', name: '运营群', avatar_url: '', target_type: 'chat' }]);
        }
        return cursor === 'p2'
          ? page([{ id: 'ou-52', name: '陈五十二', avatar_url: '', target_type: 'user' }])
          : page(
            [{ id: 'ou-21', name: '陈二十一半', avatar_url: '', target_type: 'user' }],
            { hasMore: true, cursor: 'p2' },
          );
      },
    );
    // 只派发 click（不派发 mousedown）：mousedown 会让 antd Select 失焦触发
    // onSearch('') 清空搜索词 —— 那是「点到弹窗外」的正常产品行为，不属于
    // 本用例要验证的续拉重试语义。
    await act(async () => {
      retry!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await flush(20);
    // 重试确实从断点续拉：带 cursor=p2（refresh 回第一页则不带 cursor）。
    expect(mocks.fetchFeishuTargets).toHaveBeenLastCalledWith('user', '陈', 'p2');
    expect(document.body.textContent).not.toContain('更多联系人加载失败');
    expect(editorState!.targets.some((t) => t.name === '陈五十二')).toBe(true);
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

describe('编辑器保存 — 投递开关语义（六次复审 P1-1）', () => {
  it('turning delivery OFF saves deliveries: [] — undefined would KEEP the old rows', async () => {
    // 后端 PATCH 契约：deliveries 缺失 = 不修改原有 delivery。已有投递的
    // 任务关闭开关后保存，前端必须显式发送 []（替换为空集合），否则数据
    // 库旧投递静默存活 —— 用户已关闭飞书通知，下次执行仍继续发送。
    const withDelivery: Schedule = {
      ...editingSchedule,
      deliveries: [{
        id: 1,
        target_type: 'chat',
        target_id: 'oc-9',
        target_name: '运营群',
        content_mode: 'summary',
        enabled: true,
      }],
    };
    await mountEditor({ open: true, editing: withDelivery });
    await flush(30);
    // 回填 deliveries.length > 0 → 投递自动开启。
    expect(editorState!.deliveryOn).toBe(true);

    // 关闭投递开关并保存。
    await click(document.querySelector<HTMLButtonElement>('[role="switch"]')!);
    await flush(30);
    expect(editorState!.deliveryOn).toBe(false);

    await act(async () => { await editorState!.handleOk(); });
    await flush(20);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledWith(
      9,
      expect.objectContaining({ deliveries: [] }),
    );
  });

  it('delivery left ON carries the chosen target — [] is not blanket-clearing', async () => {
    const withDelivery: Schedule = {
      ...editingSchedule,
      deliveries: [{
        id: 1,
        target_type: 'chat',
        target_id: 'oc-9',
        target_name: '运营群',
        content_mode: 'summary',
        enabled: true,
      }],
    };
    await mountEditor({ open: true, editing: withDelivery });
    await flush(30);

    await act(async () => { await editorState!.handleOk(); });
    await flush(20);
    expect(vi.mocked(updateSchedule)).toHaveBeenCalledWith(
      9,
      expect.objectContaining({
        deliveries: [expect.objectContaining({ target_id: 'oc-9' })],
      }),
    );
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
