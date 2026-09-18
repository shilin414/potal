/**
 * desktop AccessPage — deep-link + error-state regression (二次复审 P1-1).
 *
 *   · ?app=id resolves through fetchApplicationDetail when the id is NOT on
 *     the first page — the Desktop/Mobile URL contract must stay identical;
 *   · a 404/403 detail read is an explicit unavailable message; a transient
 *     failure is retryable;
 *   · a list request failure is an error state — an empty table must never
 *     masquerade as success;
 *   · a failed loadMore renders exactly ONE retry CTA, never two buttons.
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  fetchApplicationDetail: vi.fn(),
  loadMore: vi.fn(),
  refresh: vi.fn(),
}));

// Controlled hook state: each test mutates `pageState` and re-renders.
const pageState = {
  items: [] as Array<{ id: number; name: string; slug: string }>,
  loading: false,
  loadingMore: false,
  hasMore: false,
  error: null as string | null,
  loadMore: mocks.loadMore,
  refresh: mocks.refresh,
};

vi.mock('@/hooks/useApplicationPage', () => ({
  useApplicationPage: vi.fn(() => pageState),
}));

vi.mock('@/services/runApi', () => ({
  fetchApplicationDetail: mocks.fetchApplicationDetail,
}));

let routerSearch = '';
vi.mock('react-router-dom', () => ({
  useLocation: () => ({ search: routerSearch, pathname: '/enterprise/access/agents' }),
}));

vi.mock('../../enterpriseApi', () => ({
  enterpriseApi: {
    access: vi.fn(async () => ({
      application_id: 0, access_mode: 'all', departments: [], users: [],
    })),
    departments: vi.fn(async () => []),
    users: vi.fn(async () => ({ results: [], next_cursor: null })),
    updateAccess: vi.fn(),
  },
}));

vi.mock('antd', () => ({
  Alert: ({ message, description, action }: {
    message?: React.ReactNode;
    description?: React.ReactNode;
    action?: React.ReactNode;
  }) => (
    <div role="alert">
      <span>{message}</span>
      {description ? <span>{description}</span> : null}
      {action}
    </div>
  ),
  Button: ({ children, onClick }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button type="button" onClick={onClick}>{children}</button>
  ),
  Card: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  Drawer: ({ open, title, children }: {
    open?: boolean;
    title?: React.ReactNode;
    children?: React.ReactNode;
  }) => (open ? <div data-testid="drawer"><span>{title}</span>{children}</div> : null),
  Empty: ({ description }: { description?: React.ReactNode }) => (
    <div data-testid="empty">{description}</div>
  ),
  Input: Object.assign(
    (props: React.InputHTMLAttributes<HTMLInputElement>) => <input {...props} />,
    { Search: (props: React.InputHTMLAttributes<HTMLInputElement>) => <input {...props} /> },
  ),
  Radio: Object.assign(
    ({ children }: { children?: React.ReactNode }) => <label>{children}</label>,
    { Group: ({ children }: { children?: React.ReactNode }) => <div>{children}</div> },
  ),
  Select: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  Space: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  Switch: () => <button type="button" data-testid="switch" />,
  Table: ({ dataSource, locale }: {
    dataSource?: Array<{ id: number; name: string; slug: string }>;
    locale?: { emptyText?: React.ReactNode };
  }) => (
    <table data-testid="table">
      <tbody>
        {dataSource && dataSource.length > 0
          ? dataSource.map((row) => (
            <tr key={row.id} data-row={row.id}>
              <td>{row.name}</td>
              <td>{row.slug}</td>
              <td><button type="button" data-open={row.id}>设置权限</button></td>
            </tr>
          ))
          : (
            <tr><td>{locale?.emptyText}</td></tr>
          )}
      </tbody>
    </table>
  ),
  TreeSelect: () => <div />,
  Typography: { Title: ({ children }: { children?: React.ReactNode }) => <h5>{children}</h5> },
  message: { success: vi.fn(), error: vi.fn() },
}));

import AccessPage from '../AccessPage';

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
    root.render(<AccessPage kind="chat" />);
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

afterEach(async () => {
  while (mounted.length) {
    const { host, root } = mounted.pop()!;
    await act(async () => { root.unmount(); });
    host.remove();
  }
  document.body.innerHTML = '';
});

const row = (id: number, name: string) => ({ id, name, slug: `slug-${id}` });

beforeEach(() => {
  routerSearch = '';
  pageState.items = [];
  pageState.loading = false;
  pageState.loadingMore = false;
  pageState.hasMore = false;
  pageState.error = null;
  mocks.fetchApplicationDetail.mockReset();
  mocks.loadMore.mockReset();
  mocks.refresh.mockReset();
});

describe('desktop AccessPage — ?app= deep link (P1-1)', () => {
  it('opens directly when the id is on the loaded page (no detail request)', async () => {
    routerSearch = '?app=1';
    pageState.items = [row(1, '财务助手')];
    await mountPage();

    expect(mocks.fetchApplicationDetail).not.toHaveBeenCalled();
    expect(document.querySelector('[data-testid="drawer"]')!.textContent)
      .toContain('财务助手');
  });

  it('resolves an id beyond page one through fetchApplicationDetail (P1-1)', async () => {
    routerSearch = '?app=75';
    pageState.items = [row(1, '财务助手')]; // 75 only lives on page 2
    mocks.fetchApplicationDetail.mockResolvedValue({ name: '第二页智能体' });
    await mountPage();

    expect(mocks.fetchApplicationDetail).toHaveBeenCalledWith(75);
    await flush(20);
    expect(document.querySelector('[data-testid="drawer"]')!.textContent)
      .toContain('第二页智能体');
  });

  it('a 404 detail read is an explicit unavailable message (not a silent list)', async () => {
    routerSearch = '?app=999';
    pageState.items = [row(1, '财务助手')];
    mocks.fetchApplicationDetail.mockRejectedValue({ response: { status: 404 } });
    await mountPage();
    await flush(20);

    expect(document.querySelector('[data-testid="drawer"]')).toBeNull();
    expect(document.body.textContent).toContain('资源不存在或无权限');
    // The plain list is still usable.
    expect(document.body.textContent).toContain('财务助手');
  });

  it('a transient detail failure is retryable and the retry resolves (P2-6)', async () => {
    routerSearch = '?app=999';
    pageState.items = [row(1, '财务助手')];
    mocks.fetchApplicationDetail.mockRejectedValueOnce(new Error('network down'));
    await mountPage();
    await flush(20);

    expect(document.body.textContent).toContain('加载目标资源失败');
    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '重试');
    expect(retry).toBeTruthy();

    mocks.fetchApplicationDetail.mockResolvedValueOnce({ name: '恢复后的智能体' });
    await click(retry!);
    await flush(20);
    expect(mocks.fetchApplicationDetail).toHaveBeenCalledTimes(2);
    expect(document.querySelector('[data-testid="drawer"]')!.textContent)
      .toContain('恢复后的智能体');
  });
});

describe('desktop AccessPage — list error semantics (P2-11)', () => {
  it('a first-page failure is an error state, not an empty table', async () => {
    pageState.error = '网络错误';
    pageState.items = [];
    await mountPage();

    expect(document.body.textContent).toContain('加载资源失败');
    expect(document.body.textContent).not.toContain('暂无可配置的资源');
    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '重试');
    expect(retry).toBeTruthy();
    await click(retry!);
    expect(mocks.refresh).toHaveBeenCalledTimes(1);
  });

  it('a successful empty list renders the empty state', async () => {
    pageState.items = [];
    await mountPage();

    expect(document.body.textContent).toContain('暂无可配置的资源');
  });

  it('a loadMore failure keeps the table and renders exactly ONE retry CTA', async () => {
    pageState.error = '加载更多失败';
    pageState.items = [row(1, '财务助手')];
    pageState.hasMore = true;
    await mountPage();

    // Rows survive…
    expect(document.querySelector('[data-row="1"]')!.textContent).toContain('财务助手');
    // …and 加载更多 is REPLACED by the retry, never shown alongside it.
    const ctaTexts = Array.from(document.querySelectorAll('button'))
      .map((b) => b.textContent);
    expect(ctaTexts).toContain('加载失败，点击重试');
    expect(ctaTexts).not.toContain('加载更多');
    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载失败，点击重试')!;
    await click(retry);
    // hasMore is still true → the retry re-runs loadMore, not a full refresh.
    expect(mocks.loadMore).toHaveBeenCalledTimes(1);
    expect(mocks.refresh).not.toHaveBeenCalled();
  });
});
