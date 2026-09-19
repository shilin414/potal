/**
 * FeishuForwardModal — 分页失败 UX / 20 目标上限 / 部分失败保留（七次复审
 * P1-2 / P2-8 / P2-9）+ 发送会话守卫 / CTA 去重 / 部分授权失败（八次复审
 * P1 / P2）。
 *
 *   · page2 拉挂：已加载的 50 人仍然渲染，底部出现「重试加载」（调
 *     loadMore 从断点续拉），不出现整屏「搜索联系人失败」错误态，且
 *     「加载更多」与「重试加载」互斥（八次复审 P2 —— 两个按钮调的都是
 *     loadMore，不能并排出现）；
 *   · 20 目标上限：选到 20 个后第 21 个被 toggle 拒绝（warning + 计数
 *     仍为 20）—— 与 Backend feishuForwardMaxTargets、OpenAPI maxItems
 *     形成三层一致契约，不再等点「发送（21）」才吃后端 400；
 *   · 部分发送失败：成功的自动取消选择、失败的保持选中，用户可直接再
 *     点发送重试失败目标（旧实现 setSelected([]) 全清，得从头挑）；
 *   · 发送会话 ABA（八次复审 P1）：close → reopen 后旧会话的迟到响应
 *     （成功 / 部分失败 / 授权失败）不得关闭新弹窗、污染新选中、误入重
 *     新授权视图；新会话不继承旧 sending；同会话双击只发一次请求；
 *   · 部分授权失败（八次复审 P2）：1 成功 + 1 授权失败的混合结果也要展
 *     示重新授权入口，不再要求 success_count === 0。
 * @vitest-environment jsdom
 */
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  fetchFeishuTargets: vi.fn(),
  forwardShareToFeishu: vi.fn(),
  saveForwardHistory: vi.fn(),
  loadForwardHistory: vi.fn(() => []),
}));

vi.mock('@/services/shareApi', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  fetchFeishuTargets: mocks.fetchFeishuTargets,
  forwardShareToFeishu: mocks.forwardShareToFeishu,
  saveForwardHistory: mocks.saveForwardHistory,
  loadForwardHistory: mocks.loadForwardHistory,
}));

import FeishuForwardModal from '../FeishuForwardModal';
import type {
  FeishuForwardResult,
  FeishuForwardTarget,
  FeishuForwardTargetPage,
} from '@/services/shareApi';

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

/** antd 按钮两个中文字符会自动插空格 —— 统一压平后比较。 */
const btnText = (el: Element) => (el.textContent || '').replace(/\s+/g, '');

const findButton = (label: string) => (
  Array.from(document.querySelectorAll<HTMLButtonElement>('button'))
    .find((b) => btnText(b) === label));

const target = (id: string, name: string, type: 'user' | 'chat'): FeishuForwardTarget => ({
  id, name, avatar_url: '', target_type: type,
});

const page = (
  items: FeishuForwardTarget[],
  opts?: { hasMore?: boolean; cursor?: string },
): FeishuForwardTargetPage => ({
  items,
  next_cursor: opts?.cursor ?? '',
  has_more: opts?.hasMore ?? false,
});

let host: HTMLElement;
let root: Root;
let closed = false;

/** 可控的挂起 Promise —— 模拟「发送在途」的窗口。 */
function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
}

/** 用指定 props 渲染（ABA 用例需要切换 open / shareToken 模拟会话切换）。 */
async function renderModal(props: { open: boolean; shareToken: string | null }) {
  await act(async () => {
    root.render(
      <FeishuForwardModal
        open={props.open}
        shareToken={props.shareToken}
        onClose={() => { closed = true; }}
      />,
    );
  });
  await flush(10);
}

async function mountModal() {
  closed = false;
  await renderModal({ open: true, shareToken: 'share-tok' });
}

/**
 * ABA 共用前置（八次复审 §14）：打开 A → 选中目标 → 点发送（挂起）→
 * 取消关闭 A → 打开 B 并选中 B 的目标。返回 A 的 deferred send，供用例
 * 在 B 会话进行中放行 A 的迟到响应。
 */
async function sendPendingThenReopen() {
  const sendA = deferred<FeishuForwardResult>();
  mocks.fetchFeishuTargets.mockResolvedValue(page([target('oc-A1', 'A群', 'chat')]));
  await renderModal({ open: true, shareToken: 'share-A' });
  await click(document.querySelector<HTMLButtonElement>('.ffm-row')!);
  mocks.forwardShareToFeishu.mockReturnValueOnce(sendA.promise);
  await click(findButton('发送（1）')!);

  // 关闭 A（发送仍在途 —— 取消按钮不禁用）。
  await click(findButton('取消')!);
  expect(closed).toBe(true);
  await renderModal({ open: false, shareToken: 'share-A' });

  // 打开 B（新会话：新 shareToken），选中 B 的目标。
  closed = false;
  mocks.fetchFeishuTargets.mockResolvedValue(page([target('oc-B1', 'B群', 'chat')]));
  await renderModal({ open: true, shareToken: 'share-B' });
  await click(document.querySelector<HTMLButtonElement>('.ffm-row')!);
  expect(findButton('发送（1）')).toBeTruthy();
  return sendA;
}

/** 切到「联系人」tab 并输入搜索词，等服务端搜索第一页落地。 */
async function searchUsers(query: string) {
  const userTab = Array.from(document.querySelectorAll<HTMLElement>('.ant-tabs-tab'))
    .find((t) => t.textContent?.includes('联系人'));
  expect(userTab, '未找到联系人 tab').toBeTruthy();
  await click(userTab!);
  await flush(10);
  const input = document.querySelector<HTMLInputElement>('input[placeholder="输入姓名搜索联系人"]');
  expect(input, '未找到搜索输入框').toBeTruthy();
  await act(async () => { setNativeValue(input!, query); });
  await flush(350); // 300ms 防抖到期
}

beforeEach(() => {
  mocks.fetchFeishuTargets.mockReset();
  mocks.forwardShareToFeishu.mockReset();
  mocks.saveForwardHistory.mockClear();
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => { root.unmount(); });
  host.remove();
  document.body.innerHTML = '';
});

describe('FeishuForwardModal — 分页失败不吞已加载页（七次复审 P1-2）', () => {
  it('50 people loaded, page2 fails: rows stay visible + tail retry loads the FAILED page', async () => {
    // chat tab 在弹窗打开时会先发一次全量请求 —— 按 type 分流 mock，user
    // page1 成功（50 人 + hasMore），page2（cursor=p2）失败。
    mocks.fetchFeishuTargets.mockImplementation(
      async (type: 'user' | 'chat', _query?: string, cursor?: string) => {
        if (type === 'chat') return page([]);
        if (cursor === 'p2') {
          throw { response: { status: 502, data: { error: '搜索联系人失败' } } };
        }
        return page(
          Array.from({ length: 50 }, (_, i) => target(`u${i + 1}`, `员工${i + 1}`, 'user')),
          { hasMore: true, cursor: 'p2' },
        );
      },
    );
    await mountModal();
    await searchUsers('员工');

    const rows = () => document.querySelectorAll('.ffm-row');
    expect(rows().length).toBe(50);
    // page2 失败。
    const loadMoreBtn = document.querySelector<HTMLButtonElement>('.ffm-load-more');
    expect(loadMoreBtn, '加载更多入口').toBeTruthy();
    await click(loadMoreBtn!);
    await flush(10);

    // 50 人仍在，且没有整屏错误态（旧实现会把整屏换成「搜索联系人失败」
    // + 空列表 —— 错误文案本身允许出现在底部分页错误条里）。
    expect(rows().length).toBe(50);
    expect(document.querySelector('.ffm-list__error')).toBeNull();
    // 底部分页错误 + 从断点重试（重试加载 → loadMore，不是 refresh 回首页）。
    const retry = findButton('重试加载');
    expect(retry, '底部「重试加载」入口').toBeTruthy();
    // CTA 互斥（八次复审 P2）：失败期间「加载更多」必须消失 —— 两个按钮
    // 调的都是 loadMore，并排出现只是重复动作入口。
    expect(document.querySelector('.ffm-load-more')).toBeNull();

    // 重试成功：p2 断点续拉，51 人可见，错误入口消失。
    mocks.fetchFeishuTargets.mockImplementation(
      async (type: 'user' | 'chat', _query?: string, cursor?: string) => {
        if (type === 'chat') return page([]);
        return cursor === 'p2'
          ? page([target('u51', '第五十一人', 'user')], { hasMore: false })
          : page(
            Array.from({ length: 50 }, (_, i) => target(`u${i + 1}`, `员工${i + 1}`, 'user')),
            { hasMore: true, cursor: 'p2' },
          );
      },
    );
    await click(retry!);
    await flush(10);
    expect(mocks.fetchFeishuTargets).toHaveBeenLastCalledWith('user', '员工', 'p2');
    expect(rows().length).toBe(51);
    expect(findButton('重试加载')).toBeUndefined(); // 成功后错误入口消失
  });

  it('a loadMore 403 still routes the user to re-authorization', async () => {
    // 续拉 403 = 授权权限变化（七次复审 §23）：拆了 error phase 也不能丢
    // 重新授权入口。
    mocks.fetchFeishuTargets.mockImplementation(
      async (type: 'user' | 'chat', _query?: string, cursor?: string) => {
        if (type === 'chat') return page([]);
        if (cursor === 'p2') {
          throw { response: { status: 403, data: { detail: '飞书权限不足' } } };
        }
        return page([target('u1', '张一', 'user')], { hasMore: true, cursor: 'p2' });
      },
    );
    await mountModal();
    await searchUsers('张');

    await click(document.querySelector<HTMLButtonElement>('.ffm-load-more')!);
    await flush(10);
    // 整个弹窗切到重新授权引导（比「入口不丢」更强的行为），403 不再被
    // 分页错误吞掉。
    expect(document.body.textContent).toContain('重新授权飞书');
    expect(document.querySelector('.ffm-reauth')).toBeTruthy();
  });
});

describe('FeishuForwardModal — 20 目标上限（七次复审 P2-8）', () => {
  it('selecting target #21 is rejected in the UI — the count stays at 20', async () => {
    // 25 个群聊候选（chat 全量单页）。
    mocks.fetchFeishuTargets.mockResolvedValue(page(
      Array.from({ length: 25 }, (_, i) => target(`oc-${i + 1}`, `群${i + 1}`, 'chat')),
    ));
    await mountModal();
    await flush(10);
    expect(document.querySelectorAll('.ffm-row').length).toBe(25);

    const rows = Array.from(document.querySelectorAll<HTMLButtonElement>('.ffm-row'));
    // 前 20 个全部选中。
    for (const row of rows.slice(0, 20)) {
      await click(row);
    }
    const sendButton = findButton('发送（20）');
    expect(sendButton, '选中 20 个后发送按钮计数').toBeTruthy();

    // 第 21 个被 toggle 拒绝：计数仍为 20（后端 feishuForwardMaxTargets
    // 不会再见到 >20 的请求体）。
    await click(rows[20]);
    await flush(10);
    expect(findButton('发送（20）')).toBeTruthy();
    expect(findButton('发送（21）')).toBeUndefined();
    expect(document.body.textContent).toContain('一次最多转发给 20 个目标');

    // 取消一个后可以再选。
    await click(rows[0]);
    await flush(10);
    expect(findButton('发送（19）')).toBeTruthy();
    await click(rows[20]);
    await flush(10);
    expect(findButton('发送（20）')).toBeTruthy();
  });

  it('forwarding is never called with more than 20 targets', async () => {
    mocks.fetchFeishuTargets.mockResolvedValue(page(
      Array.from({ length: 25 }, (_, i) => target(`oc-${i + 1}`, `群${i + 1}`, 'chat')),
    ));
    await mountModal();
    await flush(10);
    const rows = Array.from(document.querySelectorAll<HTMLButtonElement>('.ffm-row'));
    for (const row of rows.slice(0, 20)) {
      await click(row);
    }
    await click(rows[20]); // 拒绝
    mocks.forwardShareToFeishu.mockResolvedValue({
      results: [], success_count: 0, fail_count: 0,
    });
    await click(findButton('发送（20）')!);
    await flush(10);
    expect(mocks.forwardShareToFeishu).toHaveBeenCalledTimes(1);
    const sent = mocks.forwardShareToFeishu.mock.calls[0][1] as unknown[];
    expect(sent).toHaveLength(20);
  });
});

describe('FeishuForwardModal — 部分失败保留失败目标（七次复审 P2-9）', () => {
  it('after a partial failure only the FAILED targets stay selected', async () => {
    mocks.fetchFeishuTargets.mockResolvedValue(page([
      target('oc-1', '成功群', 'chat'),
      target('oc-2', '失败群', 'chat'),
    ]));
    await mountModal();
    await flush(10);
    const rows = Array.from(document.querySelectorAll<HTMLButtonElement>('.ffm-row'));
    await click(rows[0]);
    await click(rows[1]);

    mocks.forwardShareToFeishu.mockResolvedValue({
      results: [
        { target_id: 'oc-1', ok: true },
        { target_id: 'oc-2', ok: false, error: '发送失败' },
      ],
      success_count: 1,
      fail_count: 1,
    });
    await click(findButton('发送（2）')!);
    await flush(10);

    // 成功的自动取消选择、失败的保持选中：计数 = 1，用户可直接再点发送
    // 重试失败目标（旧实现 setSelected([]) 全清，得从头挑一遍）。
    expect(findButton('发送（1）')).toBeTruthy();
    expect(closed).toBe(false); // 弹窗保持打开
    // 选中的是失败的那个（成功群不在选中态，失败群在）。
    expect(rows[0].className).not.toContain('ffm-row--on');
    expect(rows[1].className).toContain('ffm-row--on');
  });
});

describe('FeishuForwardModal — 发送会话 ABA（八次复审 P1）', () => {
  it('a stale success from session A must not close the freshly opened session B', async () => {
    const sendA = await sendPendingThenReopen();

    // A 的发送此时才返回成功（B 已经打开并选中了自己的目标）。
    await act(async () => {
      sendA.resolve({
        results: [{ target_id: 'oc-A1', ok: true }],
        success_count: 1,
        fail_count: 0,
      });
    });
    await flush(10);

    // B 弹窗保持打开、B 的选中不受影响 —— 旧实现会 message.success +
    // close() 把刚打开的 B 直接关掉。
    expect(closed).toBe(false);
    expect(findButton('发送（1）')).toBeTruthy();
    expect(document.querySelector('.ffm-row')!.className).toContain('ffm-row--on');
  });

  it('a stale partial failure from session A must not mutate session B selection', async () => {
    const sendA = await sendPendingThenReopen();

    await act(async () => {
      sendA.resolve({
        results: [{ target_id: 'oc-A1', ok: false, error: '发送失败' }],
        success_count: 0,
        fail_count: 1,
      });
    });
    await flush(10);

    // B 的选中保持原样 —— 旧实现 setSelected(A 的失败目标) 会把 B 的选中
    // 覆盖成 A 的 [A群]，B 的行当场失去选中态。
    expect(closed).toBe(false);
    expect(document.querySelector('.ffm-row')!.className).toContain('ffm-row--on');
    expect(findButton('发送（1）')).toBeTruthy();
  });

  it('a stale authorization failure from session A must not force session B into re-auth', async () => {
    const sendA = await sendPendingThenReopen();

    await act(async () => {
      sendA.resolve({
        results: [{ target_id: 'oc-A1', ok: false, error: '飞书权限不足，需要重新授权' }],
        success_count: 0,
        fail_count: 1,
      });
    });
    await flush(10);

    // B 不进入重新授权视图 —— 旧实现 setNeedReauth(true) 会让 B 的整个
    // picker 被替换成重新授权引导。
    expect(document.querySelector('.ffm-reauth')).toBeNull();
    expect(document.body.textContent).not.toContain('重新授权飞书');
    expect(findButton('发送（1）')).toBeTruthy();
  });

  it('session B must not inherit sending=true from a still-pending session A send', async () => {
    const sendA = await sendPendingThenReopen();

    // B 的发送按钮不在「发送中…」态，可以立即发送（旧实现 B 继承
    // sending=true，按钮一直 disabled 到 A 请求结束）。
    expect(findButton('发送（1）')).toBeTruthy();
    expect(findButton('发送中…')).toBeUndefined();

    const sendB = deferred<FeishuForwardResult>();
    mocks.forwardShareToFeishu.mockReturnValueOnce(sendB.promise);
    await click(findButton('发送（1）')!);
    expect(mocks.forwardShareToFeishu).toHaveBeenCalledTimes(2);

    // 收尾：两个会话的请求都放行，不留挂起的 microtask。
    await act(async () => {
      sendA.resolve({ results: [{ target_id: 'oc-A1', ok: true }], success_count: 1, fail_count: 0 });
      sendB.resolve({ results: [{ target_id: 'oc-B1', ok: true }], success_count: 1, fail_count: 0 });
    });
    await flush(10);
  });

  it('double-clicking send in the same session fires only one API request', async () => {
    mocks.fetchFeishuTargets.mockResolvedValue(page([target('oc-1', '群1', 'chat')]));
    await mountModal();
    await click(document.querySelector<HTMLButtonElement>('.ffm-row')!);

    const send = deferred<FeishuForwardResult>();
    mocks.forwardShareToFeishu.mockReturnValue(send.promise);
    const sendBtn = findButton('发送（1）')!;
    // 同一帧内连点两次 —— single-flight 拒绝第二次触发，只发一次请求。
    await act(async () => {
      sendBtn.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
      sendBtn.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
      sendBtn.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      sendBtn.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
      sendBtn.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
      sendBtn.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await flush(10);
    expect(mocks.forwardShareToFeishu).toHaveBeenCalledTimes(1);

    await act(async () => {
      send.resolve({ results: [{ target_id: 'oc-1', ok: true }], success_count: 1, fail_count: 0 });
    });
    await flush(10);
  });
});

describe('FeishuForwardModal — 部分授权失败也进入重新授权（八次复审 P2）', () => {
  it('1 success + 1 re-auth failure shows the re-authorization CTA', async () => {
    mocks.fetchFeishuTargets.mockResolvedValue(page([
      target('oc-1', '成功群', 'chat'),
      target('oc-2', '授权失败群', 'chat'),
    ]));
    await mountModal();
    const rows = Array.from(document.querySelectorAll<HTMLButtonElement>('.ffm-row'));
    await click(rows[0]);
    await click(rows[1]);

    mocks.forwardShareToFeishu.mockResolvedValue({
      results: [
        { target_id: 'oc-1', ok: true },
        { target_id: 'oc-2', ok: false, error: '飞书权限不足，需要重新授权' },
      ],
      success_count: 1,
      fail_count: 1,
    });
    await click(findButton('发送（2）')!);
    await flush(10);

    // 混合结果（success_count=1）同样展示重新授权入口 —— 旧条件
    // success_count === 0 让用户只能对着注定失败的目标反复重试。
    expect(document.querySelector('.ffm-reauth')).toBeTruthy();
    expect(document.body.textContent).toContain('重新授权飞书');
  });
});
