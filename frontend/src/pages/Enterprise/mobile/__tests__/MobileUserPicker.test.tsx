/**
 * MobileUserPicker — 数据边界与错误态回归（二次复审 P2-5）。
 *
 *   · 数据面复用 useDirectoryUsers：服务端搜索 + cursor 加载更多，不再被
 *     100 人截断；ACL 选人只看有效员工（不传 include_inactive）；
 *   · 请求失败是错误态 + 重试，绝不伪装成“没有匹配的人员”；
 *   · 跨页选择保留：翻页后已选的人保持选中，完成时全部带回；
 *   · loadMore 失败只有一个重试 CTA（替换加载更多，不并列）。
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  users: vi.fn(),
}));

vi.mock('../../enterpriseApi', () => ({
  enterpriseApi: {
    users: mocks.users,
  },
}));

vi.mock('antd', () => ({
  Avatar: ({ children }: { children?: React.ReactNode }) => (
    <span data-testid="avatar">{children}</span>
  ),
  Button: ({ children, onClick }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button type="button" onClick={onClick}>{children}</button>
  ),
  Drawer: ({ open, children }: { open?: boolean; children?: React.ReactNode }) => (
    open ? <div data-testid="drawer">{children}</div> : null
  ),
  Input: (props: React.InputHTMLAttributes<HTMLInputElement>) => <input {...props} />,
}));

import MobileUserPicker from '../MobileUserPicker';

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

const mounted: Array<{ host: HTMLElement; root: Root }> = [];
async function mountPicker(props: Partial<Parameters<typeof MobileUserPicker>[0]> = {}) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  mounted.push({ host, root });
  await act(async () => {
    root.render(
      <MobileUserPicker
        open
        selectedUsers={[]}
        onClose={() => {}}
        onDone={() => {}}
        {...props}
      />,
    );
  });
  await flush(20);
  return { host, root };
}

function click(el: Element) {
  return act(async () => {
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

function setNativeValue(el: HTMLInputElement, value: string) {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!
    .set!.call(el, value);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}

afterEach(async () => {
  while (mounted.length) {
    const { host, root } = mounted.pop()!;
    await act(async () => { root.unmount(); });
    host.remove();
  }
  document.body.innerHTML = '';
});

const user = (id: number, name: string) => ({
  id, name, avatar_url: '', open_id: `o-${id}`, active_status: 1,
  is_resigned: false, local_user_id: null, is_active: true,
  departments: [{ id: 1, name: '财务部', is_primary: true }],
});

beforeEach(() => {
  mocks.users.mockReset().mockResolvedValue({
    results: [user(1, '张三'), user(2, '李四')],
    next_cursor: 'cursor-1',
  });
});

describe('MobileUserPicker — request contract (P2-5)', () => {
  it('loads active users only — no include_inactive param (ACL grants active employees)', async () => {
    await mountPicker();
    expect(mocks.users).toHaveBeenCalledWith(
      expect.not.objectContaining({ include_inactive: true }),
    );
    const names = Array.from(document.querySelectorAll('.mobile-picker__row-name'))
      .map((el) => el.textContent);
    expect(names).toContain('张三');
  });

  it('sends the search term to the server (debounced)', async () => {
    await mountPicker();
    const input = document.querySelector<HTMLInputElement>('.mobile-picker__search input')!;
    await act(async () => { setNativeValue(input, '陈'); });
    await flush(50);
    expect(mocks.users).toHaveBeenCalledTimes(1); // still inside the debounce
    await flush(350);
    expect(mocks.users).toHaveBeenLastCalledWith(
      expect.objectContaining({ q: '陈' }),
    );
  });
});

describe('MobileUserPicker — cursor pagination (P2-5)', () => {
  it('next_cursor renders 加载更多 and the next call carries the cursor', async () => {
    await mountPicker();
    const more = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载更多');
    expect(more).toBeTruthy();

    mocks.users.mockResolvedValueOnce({
      results: [user(3, '王五'), user(4, '赵六')],
      next_cursor: null,
    });
    await click(more!);
    await flush(20);

    expect(mocks.users).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'cursor-1' }),
    );
    const names = Array.from(document.querySelectorAll('.mobile-picker__row-name'))
      .map((el) => el.textContent);
    expect(names).toContain('王五');
    expect(names).toContain('张三'); // page one survives the append
    // Cursor exhausted → no more 加载更多 button.
    expect(Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载更多')).toBeUndefined();
  });

  it('selection survives across pages and 完成 returns everyone picked', async () => {
    const onDone = vi.fn();
    await mountPicker({ onDone });

    await click(Array.from(document.querySelectorAll('.mobile-picker__row'))
      .find((el) => el.textContent!.includes('张三'))!);

    mocks.users.mockResolvedValueOnce({ results: [user(3, '王五')], next_cursor: null });
    await click(Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载更多')!);
    await flush(20);

    await click(Array.from(document.querySelectorAll('.mobile-picker__row'))
      .find((el) => el.textContent!.includes('王五'))!);

    expect(document.body.textContent).toContain('已选择 2 人');
    await click(Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '完成')!);
    expect(onDone).toHaveBeenCalledWith([
      expect.objectContaining({ id: 1, name: '张三' }),
      expect.objectContaining({ id: 3, name: '王五' }),
    ]);
  });

  it('a loadMore failure keeps the rows and renders exactly ONE retry CTA', async () => {
    await mountPicker();
    mocks.users.mockRejectedValueOnce(new Error('network down'));
    await click(Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载更多')!);
    await flush(20);

    const ctaTexts = Array.from(document.querySelectorAll('button'))
      .map((b) => b.textContent);
    expect(ctaTexts).toContain('加载失败，点击重试');
    expect(ctaTexts).not.toContain('加载更多');
    expect(document.body.textContent).toContain('张三');
  });

  it('a failed SEARCH keeps the rows and the retry refreshes page one — not a dead loadMore', async () => {
    await mountPicker();
    const input = document.querySelector<HTMLInputElement>('.mobile-picker__search input')!;
    await act(async () => { setNativeValue(input, '陈'); });
    // The debounced search request fails — old rows survive, cursor is cleared.
    mocks.users.mockRejectedValueOnce(new Error('network down'));
    await flush(400);

    const cta = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载失败，点击重试');
    expect(cta).toBeTruthy();
    expect(document.body.textContent).toContain('张三');

    // The retry must go through refresh (hasMore=false, no cursor left) and
    // re-run the search server-side.
    mocks.users.mockResolvedValueOnce({ results: [user(5, '陈皮')], next_cursor: null });
    await click(cta!);
    await flush(20);

    expect(mocks.users).toHaveBeenLastCalledWith(
      expect.objectContaining({ q: '陈' }),
    );
    expect(document.body.textContent).toContain('陈皮');
    expect(document.body.textContent).not.toContain('张三');
  });
});

describe('MobileUserPicker — error states (P2-5)', () => {
  it('a first-page failure is an error state with retry, never 没有匹配的人员', async () => {
    mocks.users.mockRejectedValue(new Error('network down'));
    await mountPicker();

    expect(document.body.textContent).toContain('加载人员失败');
    expect(document.body.textContent).not.toContain('没有匹配的人员');
    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '重试');
    expect(retry).toBeTruthy();

    mocks.users.mockResolvedValue({ results: [user(1, '张三')], next_cursor: null });
    await click(retry!);
    await flush(20);
    expect(document.body.textContent).toContain('张三');
  });

  it('an empty successful result is the empty state, not an error', async () => {
    mocks.users.mockResolvedValue({ results: [], next_cursor: null });
    await mountPicker();
    expect(document.body.textContent).toContain('没有匹配的人员');
    expect(document.body.textContent).not.toContain('加载人员失败');
  });

  it('selectedUsers from the policy stay pre-picked on open', async () => {
    await mountPicker({
      selectedUsers: [{ id: 1, name: '张三', avatar_url: '', departments: ['财务部'] }],
    });
    expect(document.body.textContent).toContain('已选择 1 人');
    const row = Array.from(document.querySelectorAll('.mobile-picker__row'))
      .find((el) => el.textContent!.includes('张三'))!;
    expect(row.getAttribute('aria-pressed')).toBe('true');
  });
});

describe('MobileUserPicker — close resets the query (三次复审 §46)', () => {
  it('reopening never fires a request carrying the previous search term', async () => {
    const { root } = await mountPicker();
    const input = document.querySelector('input')!;
    await act(async () => { setNativeValue(input, '张'); });
    await flush(350);
    expect(mocks.users).toHaveBeenLastCalledWith(
      expect.objectContaining({ q: '张' }));

    // Close → the query clears immediately (no stale term survives).
    await act(async () => {
      root.render(
        <MobileUserPicker
          open={false}
          selectedUsers={[]}
          onClose={() => {}}
          onDone={() => {}}
        />,
      );
    });
    await flush(350);

    // Reopen → page one must NOT carry the previous term.
    mocks.users.mockClear();
    await act(async () => {
      root.render(
        <MobileUserPicker
          open
          selectedUsers={[]}
          onClose={() => {}}
          onDone={() => {}}
        />,
      );
    });
    await flush(350);
    expect(mocks.users).toHaveBeenCalled();
    expect(mocks.users).not.toHaveBeenCalledWith(
      expect.objectContaining({ q: '张' }));
  });
});
