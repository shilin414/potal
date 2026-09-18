/**
 * MobileDirectoryPage — 数据边界回归（二次复审 P1-4）。
 *
 * Pins the rules the 1.0 review found broken:
 *   · department search is LOCAL ONLY — typing on the departments tab must not
 *     fire a users request (the old shared `q` re-requested both every time);
 *   · users tab: server search (debounced) + cursor 加载更多 (>100 people);
 *   · the two tabs own independent queries — switching does not carry the term;
 *   · a request failure is an error state, not 没有匹配的人员.
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  departments: vi.fn(),
  users: vi.fn(),
}));

vi.mock('../../enterpriseApi', () => ({
  enterpriseApi: {
    departments: mocks.departments,
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
  Input: (props: React.InputHTMLAttributes<HTMLInputElement>) => <input {...props} />,
  Segmented: ({ value, onChange, options }: {
    value: string;
    onChange: (v: string) => void;
    options: Array<{ value: string; label: string }>;
  }) => (
    <div data-testid="segmented">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          data-value={o.value}
          className={o.value === value ? 'active' : ''}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  ),
  Skeleton: () => <div data-testid="skeleton" />,
  Tag: ({ children }: { children?: React.ReactNode }) => (
    <span data-testid="tag">{children}</span>
  ),
}));

import MobileDirectoryPage from '../MobileDirectoryPage';

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
async function mountPage() {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  mounted.push({ host, root });
  await act(async () => {
    root.render(<MobileDirectoryPage />);
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

const dep = (id: number, name: string) => ({
  id, name, is_active: true, parent_id: null, parent_open_department_id: '',
  open_department_id: `od-${id}`, order_weight: '0',
});

const user = (id: number, name: string) => ({
  id, name, avatar_url: '', open_id: `o-${id}`, active_status: 1,
  is_resigned: false, local_user_id: null, is_active: true,
  departments: [{ id: 1, name: '财务部', is_primary: true }],
});

beforeEach(() => {
  mocks.departments.mockReset().mockResolvedValue([dep(1, '财务部'), dep(2, '技术部')]);
  mocks.users.mockReset().mockResolvedValue({
    results: [user(1, '张三'), user(2, '李四')],
    next_cursor: 'cursor-1',
  });
});

describe('MobileDirectoryPage — tab-split queries (P1-4)', () => {
  it('loads departments once and searches them locally — no users request on the departments tab', async () => {
    await mountPage();
    expect(mocks.departments).toHaveBeenCalledTimes(1);
    expect(mocks.users).not.toHaveBeenCalled(); // users tab never entered

    const input = document.querySelector<HTMLInputElement>('.mobile-console-search input')!;
    await act(async () => { setNativeValue(input, '财务'); });
    await flush(20);

    // Local filter narrows…
    const names = Array.from(document.querySelectorAll('.mobile-console-row__title'))
      .map((el) => el.textContent);
    expect(names).toEqual(['财务部']);
    // …without a single extra request.
    expect(mocks.departments).toHaveBeenCalledTimes(1);
    expect(mocks.users).not.toHaveBeenCalled();
  });

  it('switching to the users tab searches server-side with debounce', async () => {
    await mountPage();
    await click(document.querySelector('[data-value="users"]')!);
    expect(mocks.users).toHaveBeenCalledWith(expect.objectContaining({ limit: 50 }));

    const input = document.querySelector<HTMLInputElement>('.mobile-console-search input')!;
    await act(async () => { setNativeValue(input, '张'); });
    await flush(50);
    expect(mocks.users).toHaveBeenCalledTimes(1); // still inside the debounce
    await flush(350);
    expect(mocks.users).toHaveBeenCalledTimes(2);
    expect(mocks.users).toHaveBeenLastCalledWith(
      expect.objectContaining({ q: '张', include_inactive: true, limit: 50 }),
    );
  });

  it('tab switches do NOT inherit each other\'s query', async () => {
    await mountPage();
    // Type on departments first.
    let input = document.querySelector<HTMLInputElement>('.mobile-console-search input')!;
    await act(async () => { setNativeValue(input, '财务'); });

    // Switch to users — the search box starts empty.
    await click(document.querySelector('[data-value="users"]')!);
    input = document.querySelector<HTMLInputElement>('.mobile-console-search input')!;
    expect(input.value).toBe('');
    // The users request is not narrowed by the departments term.
    expect(mocks.users).toHaveBeenCalledWith(
      expect.not.objectContaining({ q: '财务' }),
    );

    // And back to departments — its own term survives.
    await click(document.querySelector('[data-value="departments"]')!);
    input = document.querySelector<HTMLInputElement>('.mobile-console-search input')!;
    expect(input.value).toBe('财务');
  });
});

describe('MobileDirectoryPage — users cursor pagination (P1-4)', () => {
  it('next_cursor renders 加载更多 and the next call carries the cursor', async () => {
    await mountPage();
    await click(document.querySelector('[data-value="users"]')!);
    await flush(20);

    const names = Array.from(document.querySelectorAll('.mobile-console-row__title'))
      .map((el) => el.textContent);
    expect(names).toContain('张三');

    const more = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载更多');
    expect(more).toBeTruthy();

    mocks.users.mockResolvedValueOnce({
      results: [user(3, '王五')],
      next_cursor: null,
    });
    await click(more!);
    await flush(20);

    expect(mocks.users).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'cursor-1' }),
    );
    const namesAfter = Array.from(document.querySelectorAll('.mobile-console-row__title'))
      .map((el) => el.textContent);
    expect(namesAfter).toContain('王五');
    // The 加载更多 button disappears when next_cursor is exhausted.
    expect(Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载更多')).toBeUndefined();
  });

  it('the users tab does not show a fake total count', async () => {
    await mountPage();
    const usersTab = document.querySelector('[data-value="users"]')!;
    expect(usersTab.textContent).toBe('人员');
  });
});

describe('MobileDirectoryPage — error states (P1-4)', () => {
  it('a departments failure is an error state with retry', async () => {
    mocks.departments.mockRejectedValue(new Error('network down'));
    await mountPage();

    expect(document.body.textContent).toContain('加载部门失败');
    expect(Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '重试')).toBeTruthy();
  });

  it('a users failure is an error state, not 没有匹配的人员', async () => {
    mocks.users.mockRejectedValue(new Error('network down'));
    await mountPage();
    await click(document.querySelector('[data-value="users"]')!);
    await flush(20);

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
});
