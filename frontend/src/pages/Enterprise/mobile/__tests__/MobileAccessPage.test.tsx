/**
 * MobileAccessPage — 数据边界与深链回归（二次复审 P1-1/P2-11）。
 *
 * Pin the three rules the 1.0 review found broken:
 *   · the resource list is really paged (hasMore → loadMore, second page renders);
 *   · ?app=id resolves through fetchApplicationDetail when the id is NOT on the
 *     first page — the deep link from 资源管理 must never silently degrade into
 *     a plain list, and a missing/unauthorized id must say so;
 *   · a request failure is an error state — never 暂无可配置的资源.
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
  items: [] as Array<{ id: number; name: string; slug: string; color?: string }>,
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

vi.mock('@/components/Agents/AgentAvatar', () => ({
  default: ({ application }: { application: { name: string } }) => (
    <span data-testid="avatar">{application.name}</span>
  ),
}));

vi.mock('antd', () => ({
  Alert: ({ message, action }: {
    message?: React.ReactNode;
    action?: React.ReactNode;
  }) => (
    <div role="alert">{message}{action}</div>
  ),
  Button: ({ children, onClick }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button type="button" onClick={onClick}>{children}</button>
  ),
  Input: (props: React.InputHTMLAttributes<HTMLInputElement>) => <input {...props} />,
  Skeleton: () => <div data-testid="skeleton" />,
}));

vi.mock('../MobilePermissionEditor', () => ({
  default: ({ open, application }: {
    open: boolean;
    application: { id: number; name: string } | null;
  }) => (
    open
      ? <div data-testid="permission-editor">{application ? `${application.id}:${application.name}` : ''}</div>
      : null
  ),
}));

import MobileAccessPage from '../MobileAccessPage';

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
    root.render(<MobileAccessPage kind="chat" />);
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

describe('MobileAccessPage — list & pagination (P1-1)', () => {
  it('renders the first page as rows', async () => {
    pageState.items = [row(1, '财务助手'), row(2, 'IT助手')];
    await mountPage();
    const titles = Array.from(document.querySelectorAll('.mobile-console-row__title'))
      .map((el) => el.textContent);
    expect(titles).toContain('财务助手');
    expect(titles).toContain('IT助手');
  });

  it('hasMore renders 加载更多 and clicking it calls loadMore (P1-1)', async () => {
    pageState.items = [row(1, '财务助手')];
    pageState.hasMore = true;
    await mountPage();
    const more = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载更多');
    expect(more).toBeTruthy();
    await click(more!);
    expect(mocks.loadMore).toHaveBeenCalledTimes(1);
  });
});

describe('MobileAccessPage — ?app= deep link resolve (P1-1)', () => {
  it('resolves an id that is NOT on the first page via fetchApplicationDetail', async () => {
    routerSearch = '?app=999';
    pageState.items = [row(1, '财务助手')];
    mocks.fetchApplicationDetail.mockResolvedValue({ name: '远端智能体' });
    await mountPage();

    expect(mocks.fetchApplicationDetail).toHaveBeenCalledWith(999);
    await flush(20);
    expect(document.querySelector('[data-testid="permission-editor"]')!.textContent)
      .toBe('999:远端智能体');
  });

  it('opens directly when the id is on the loaded page (no detail request)', async () => {
    routerSearch = '?app=1';
    pageState.items = [row(1, '财务助手')];
    await mountPage();

    expect(mocks.fetchApplicationDetail).not.toHaveBeenCalled();
    expect(document.querySelector('[data-testid="permission-editor"]')!.textContent)
      .toBe('1:财务助手');
  });

  it('a 404 resolve surfaces 资源不存在或无权限 instead of a silent plain list', async () => {
    routerSearch = '?app=999';
    pageState.items = [row(1, '财务助手')];
    mocks.fetchApplicationDetail.mockRejectedValue({ response: { status: 404 } });
    await mountPage();
    await flush(20);

    expect(document.querySelector('[data-testid="permission-editor"]')).toBeNull();
    expect(document.body.textContent).toContain('资源不存在或无权限');
    // A 403/404 is terminal — no retry action is offered.
    expect(document.body.textContent).not.toContain('重新加载目标资源');
    // The plain list is still usable.
    expect(document.body.textContent).toContain('财务助手');
  });

  it('a TRANSIENT resolve failure is retryable and the retry resolves (P2-6)', async () => {
    routerSearch = '?app=999';
    pageState.items = [row(1, '财务助手')];
    mocks.fetchApplicationDetail.mockRejectedValueOnce(new Error('network down'));
    await mountPage();
    await flush(20);

    // A network failure is NOT framed as a permission problem…
    expect(document.body.textContent).toContain('加载目标资源失败');
    expect(document.body.textContent).not.toContain('资源不存在或无权限');
    // …and comes with a retry action.
    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '重新加载目标资源');
    expect(retry).toBeTruthy();

    mocks.fetchApplicationDetail.mockResolvedValueOnce({ name: '恢复后的智能体' });
    await click(retry!);
    await flush(20);
    expect(mocks.fetchApplicationDetail).toHaveBeenCalledTimes(2);
    expect(document.querySelector('[data-testid="permission-editor"]')!.textContent)
      .toBe('999:恢复后的智能体');
  });

  it('opening a plain list row clears a lingering deep-link error (P2-6)', async () => {
    routerSearch = '?app=999';
    pageState.items = [row(1, '财务助手')];
    mocks.fetchApplicationDetail.mockRejectedValue({ response: { status: 404 } });
    await mountPage();
    await flush(20);
    expect(document.body.textContent).toContain('资源不存在或无权限');

    await click(document.querySelector('.mobile-console-row__main')!);
    await flush(20);
    expect(document.body.textContent).not.toContain('资源不存在或无权限');
    expect(document.querySelector('[data-testid="permission-editor"]')!.textContent)
      .toBe('1:财务助手');
  });
});

describe('MobileAccessPage — error states (P2-11)', () => {
  it('a first-page failure is an error state, not 暂无可配置的资源', async () => {
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

  it('a loadMore failure keeps the rendered rows (partial error)', async () => {
    pageState.error = '加载更多失败';
    pageState.items = [row(1, '财务助手')];
    pageState.hasMore = true;
    await mountPage();

    // Rows survive…
    expect(document.querySelector('.mobile-console-row__title')!.textContent)
      .toContain('财务助手');
    // …the failure is surfaced, and exactly ONE retry CTA exists (§47–§48):
    // the Alert explains, the bottom button acts — never two retry entrypoints.
    const ctaTexts = Array.from(document.querySelectorAll('button'))
      .map((b) => b.textContent);
    expect(ctaTexts.filter((t) => t === '重试')).toHaveLength(0);
    expect(ctaTexts.filter((t) => t === '加载失败，点击重试')).toHaveLength(1);
    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载失败，点击重试');
    expect(retry).toBeTruthy();
    await click(retry!);
    // hasMore is still true → the retry re-runs loadMore, not a full refresh.
    expect(mocks.loadMore).toHaveBeenCalledTimes(1);
    expect(mocks.refresh).not.toHaveBeenCalled();
  });
});
