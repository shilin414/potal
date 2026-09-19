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
  Button: ({
    children, onClick, disabled, loading,
  }: React.ButtonHTMLAttributes<HTMLButtonElement> & { loading?: boolean }) => (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      data-loading={loading ? 'true' : 'false'}
    >
      {children}
    </button>
  ),
  Card: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  Drawer: ({
    open, title, children, extra, onClose,
  }: {
    open?: boolean;
    title?: React.ReactNode;
    children?: React.ReactNode;
    extra?: React.ReactNode;
    onClose?: () => void;
  }) => (open ? (
    <div data-testid="drawer">
      <span>{title}</span>
      <button type="button" data-testid="drawer-close" onClick={onClose}>×</button>
      {extra}{children}
    </div>
  ) : null),
  Empty: ({ description }: { description?: React.ReactNode }) => (
    <div data-testid="empty">{description}</div>
  ),
  Skeleton: ({ active }: { active?: boolean }) => (
    <div data-testid="skeleton" data-active={String(Boolean(active))} />
  ),
  Input: Object.assign(
    (props: React.InputHTMLAttributes<HTMLInputElement>) => <input {...props} />,
    { Search: (props: React.InputHTMLAttributes<HTMLInputElement>) => <input {...props} /> },
  ),
  Radio: Object.assign(
    ({ children }: { children?: React.ReactNode }) => <label>{children}</label>,
    { Group: ({ children }: { children?: React.ReactNode }) => <div>{children}</div> },
  ),
  Select: ({
    options, onSearch, onPopupScroll, loading, mode,
  }: {
    options?: Array<{ value: number; label: string }>;
    onSearch?: (value: string) => void;
    onPopupScroll?: (e: {
      currentTarget: { scrollTop: number; scrollHeight: number; clientHeight: number };
    }) => void;
    loading?: boolean;
    mode?: string;
  }) => (
    <div
      data-testid="user-select"
      data-mode={mode}
      data-loading={loading ? 'true' : 'false'}
      data-options={JSON.stringify((options ?? []).map((o) => o.value))}
    >
      <button type="button" data-testid="select-search" onClick={() => onSearch?.('张伟')}>
        search
      </button>
      <button
        type="button"
        data-testid="select-scroll"
        onClick={() => onPopupScroll?.({
          // 近底部：scrollHeight - scrollTop - clientHeight = 10 < 24。
          currentTarget: { scrollTop: 90, scrollHeight: 100, clientHeight: 0 },
        })}
      >
        scroll
      </button>
    </div>
  ),
  Space: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  Switch: () => <button type="button" data-testid="switch" />,
  Table: ({ dataSource, columns, locale }: {
    dataSource?: Array<{ id: number; name: string; slug: string }>;
    columns?: Array<{ render?: (_: unknown, row: unknown) => React.ReactNode }>;
    locale?: { emptyText?: React.ReactNode };
  }) => (
    <table data-testid="table">
      <tbody>
        {dataSource && dataSource.length > 0
          ? dataSource.map((row) => (
            <tr key={row.id} data-row={row.id}>
              <td>{row.name}</td>
              <td>{row.slug}</td>
              {/* The REAL 操作 cell — its buttons carry the live onClick. */}
              <td data-open-cell={row.id}>
                {columns?.[columns.length - 1]?.render?.(null, row)}
              </td>
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
import { enterpriseApi, type DirectoryUser } from '../../enterpriseApi';
import { message } from 'antd';

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

/** Deferred promise — lets a test dictate response ORDER (三次复审 P0). */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
}

describe('desktop AccessPage — permission target race (三次复审 P0)', () => {
  const POLICY_A = {
    application_id: 7,
    access_mode: 'assigned' as const,
    departments: [{ department_id: 3, name: 'A资源部门', include_children: true, covered_users: 12 }],
    users: [],
  };
  const POLICY_B = {
    application_id: 8,
    access_mode: 'assigned' as const,
    departments: [{ department_id: 4, name: 'B资源部门', include_children: true, covered_users: 5 }],
    users: [],
  };

  beforeEach(() => {
    vi.mocked(enterpriseApi.access).mockReset();
    vi.mocked(enterpriseApi.updateAccess).mockReset();
    vi.mocked(message.success).mockReset();
    pageState.items = [];
    pageState.error = null;
    pageState.hasMore = false;
  });

  it('a late A response cannot overwrite B — save writes B id + B grants', async () => {
    pageState.items = [row(7, 'A资源'), row(8, 'B资源')];
    const a = deferred<typeof POLICY_A>();
    const b = deferred<typeof POLICY_B>();
    vi.mocked(enterpriseApi.access).mockImplementation(
      (id: number) => (id === 7 ? a.promise : b.promise),
    );

    await mountPage();
    await click(document.querySelector('[data-open-cell="7"] button')!); // drawer for A, pending
    await click(document.querySelector('[data-open-cell="8"] button')!); // switch to B directly

    await act(async () => { b.resolve(POLICY_B); });
    await flush(20);
    const drawer = () => document.querySelector('[data-testid="drawer"]')!;
    expect(drawer().textContent).toContain('B资源部门');

    await act(async () => { a.resolve(POLICY_A); }); // late A must be ignored
    await flush(20);
    expect(drawer().textContent).toContain('B资源部门');
    expect(drawer().textContent).not.toContain('A资源部门');

    vi.mocked(enterpriseApi.updateAccess).mockResolvedValue(POLICY_B);
    const save = Array.from(document.querySelectorAll('button'))
      .find((el) => el.textContent === '保存')!;
    await click(save);
    expect(vi.mocked(enterpriseApi.updateAccess)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(enterpriseApi.updateAccess)).toHaveBeenCalledWith(8, {
      access_mode: 'assigned',
      department_grants: [{ department_id: 4, include_children: true }],
      user_grants: [],
    });
  });

  it('a slow save for A cannot toast/close over B (save target guard)', async () => {
    pageState.items = [row(7, 'A资源'), row(8, 'B资源')];
    vi.mocked(enterpriseApi.access).mockImplementation(
      (id: number) => Promise.resolve(id === 7 ? POLICY_A : POLICY_B),
    );
    const saveResult = deferred<typeof POLICY_A>();
    vi.mocked(enterpriseApi.updateAccess).mockReturnValue(saveResult.promise);

    await mountPage();
    await click(document.querySelector('[data-open-cell="7"] button')!);
    await flush(20);
    const save = () => Array.from(document.querySelectorAll('button'))
      .find((el) => el.textContent === '保存')!;
    await click(save()!);

    // While A's save is travelling, switch the drawer to B.
    await click(document.querySelector('[data-open-cell="8"] button')!);
    await flush(20);

    await act(async () => { saveResult.resolve(POLICY_A); });
    await flush(20);
    // The late A save neither toasts nor overwrites B's policy.
    expect(vi.mocked(message.success)).not.toHaveBeenCalled();
    const drawer = document.querySelector('[data-testid="drawer"]')!;
    expect(drawer.textContent).toContain('B资源部门');
    expect(drawer.textContent).not.toContain('A资源部门');
  });
});

describe('desktop AccessPage — drawer 三态 + save spinner 复位（复审）', () => {
  const POLICY_A = {
    application_id: 7,
    access_mode: 'assigned' as const,
    departments: [{ department_id: 3, name: 'A资源部门', include_children: true, covered_users: 12 }],
    users: [],
  };
  const POLICY_B = {
    application_id: 8,
    access_mode: 'assigned' as const,
    departments: [{ department_id: 4, name: 'B资源部门', include_children: true, covered_users: 5 }],
    users: [],
  };

  beforeEach(() => {
    vi.mocked(enterpriseApi.access).mockReset();
    vi.mocked(enterpriseApi.updateAccess).mockReset();
    vi.mocked(message.success).mockReset();
    pageState.items = [];
    pageState.error = null;
    pageState.hasMore = false;
  });

  it('a failed policy load shows an in-drawer error + 重试 (never an eternal Empty)', async () => {
    pageState.items = [row(7, 'A资源')];
    vi.mocked(enterpriseApi.access).mockRejectedValueOnce(new Error('network down'));
    vi.mocked(enterpriseApi.access).mockResolvedValue(POLICY_A);

    await mountPage();
    await click(document.querySelector('[data-open-cell="7"] button')!);
    await flush(20);

    const drawer = () => document.querySelector('[data-testid="drawer"]')!;
    expect(drawer().textContent).toContain('加载访问权限失败');
    // 保存 disabled until a policy is actually loaded.
    const save = Array.from(drawer().querySelectorAll('button'))
      .find((el) => el.textContent === '保存')!;
    expect(save.disabled).toBe(true);

    const retry = Array.from(drawer().querySelectorAll('button'))
      .find((el) => el.textContent === '重试')!;
    await click(retry);
    await flush(20);
    expect(vi.mocked(enterpriseApi.access)).toHaveBeenCalledTimes(2);
    expect(drawer().textContent).toContain('A资源部门');
    expect(drawer().textContent).not.toContain('加载访问权限失败');
  });

  it('a save finishing after the target switch clears the saving spinner (复审意见 #1)', async () => {
    pageState.items = [row(7, 'A资源'), row(8, 'B资源')];
    vi.mocked(enterpriseApi.access).mockImplementation(
      (id: number) => Promise.resolve(id === 7 ? POLICY_A : POLICY_B),
    );
    const saveResult = deferred<typeof POLICY_A>();
    vi.mocked(enterpriseApi.updateAccess).mockReturnValue(saveResult.promise);

    await mountPage();
    await click(document.querySelector('[data-open-cell="7"] button')!);
    await flush(20);
    const save = () => Array.from(document.querySelectorAll('button'))
      .find((el) => el.textContent === '保存')!;
    await click(save()!);
    expect(save()!.dataset.loading).toBe('true'); // spinner on while A saves

    // While A's save is travelling, switch the drawer to B.
    await click(document.querySelector('[data-open-cell="8"] button')!);
    await flush(20);

    await act(async () => { saveResult.resolve(POLICY_A); });
    await flush(20);
    // B's 保存 must not spin forever — the spinner cleared with the save.
    expect(save()!.dataset.loading).toBe('false');
    expect(save()!.disabled).toBe(false); // B loaded and idle
  });

  it('reopening for B never flashes A\'s grants under B\'s title (复审 P1)', async () => {
    pageState.items = [row(7, 'A资源'), row(8, 'B资源')];
    vi.mocked(enterpriseApi.access).mockImplementation(
      (id: number) => (id === 7
        ? Promise.resolve(POLICY_A)
        : new Promise<typeof POLICY_B>(() => {})), // B stays pending → skeleton holds
    );

    await mountPage();
    await click(document.querySelector('[data-open-cell="7"] button')!);
    await flush(20);
    const drawer = () => document.querySelector('[data-testid="drawer"]')!;
    expect(drawer().textContent).toContain('A资源部门');

    // Close A — the drawer content unmounts.
    await click(document.querySelector('[data-testid="drawer-close"]')!);
    await flush(20);
    expect(document.querySelector('[data-testid="drawer"]')).toBeNull();

    // Reopen for B: the drawer must show the loading skeleton and NEVER A's
    // departments/users (the hook cleared them while the drawer was closed).
    await click(document.querySelector('[data-open-cell="8"] button')!);
    await flush(20);
    expect(drawer().textContent).not.toContain('A资源部门');
    expect(drawer().querySelector('[data-testid="skeleton"]')).toBeTruthy();
    expect(drawer().querySelector('[data-testid="empty"]')).toBeNull();
  });

  it('a save resolving after close→reopen of the SAME id cannot touch the new session (四次复审 P1-1 ABA)', async () => {
    pageState.items = [row(7, 'A资源')];
    vi.mocked(enterpriseApi.access).mockResolvedValue(POLICY_A);
    /** What updateAccess(7) answers — the SAVED state differs from the loaded one. */
    const POLICY_A_SAVED = {
      ...POLICY_A,
      departments: [{ department_id: 3, name: 'A资源部门-已保存', include_children: true, covered_users: 12 }],
    };
    const saveResult = deferred<typeof POLICY_A_SAVED>();
    vi.mocked(enterpriseApi.updateAccess).mockReturnValue(saveResult.promise);

    await mountPage();
    await click(document.querySelector('[data-open-cell="7"] button')!);
    await flush(20);
    const save = () => Array.from(document.querySelectorAll('button'))
      .find((el) => el.textContent === '保存')!;
    await click(save()!);
    expect(save()!.dataset.loading).toBe('true'); // session #1 save spinner on

    // Close the drawer while the save is still travelling.
    await click(document.querySelector('[data-testid="drawer-close"]')!);
    await flush(20);
    expect(document.querySelector('[data-testid="drawer"]')).toBeNull();

    // Reopen A — a NEW session with the SAME id loads its own fresh policy…
    await click(document.querySelector('[data-open-cell="7"] button')!);
    await flush(20);
    const drawer = () => document.querySelector('[data-testid="drawer"]')!;
    expect(drawer().textContent).toContain('A资源部门');
    // …and its 保存 button is NOT spinning: the old session's save released
    // the spinner the moment the drawer closed/reopened (P1-2).
    expect(save()!.dataset.loading).toBe('false');

    // The old save settles NOW — same id (A → null → A) but a different
    // session epoch: no toast, no overwrite of the fresh policy.
    await act(async () => { saveResult.resolve(POLICY_A_SAVED); });
    await flush(20);
    expect(vi.mocked(message.success)).not.toHaveBeenCalled();
    expect(drawer().textContent).toContain('A资源部门');
    expect(drawer().textContent).not.toContain('A资源部门-已保存');
    expect(save()!.dataset.loading).toBe('false');
  });
});

describe('desktop AccessPage — user picker server search + pagination (四次复审 P2-2)', () => {
  const dirUser = (id: number): DirectoryUser => ({
    id, name: `员工${id}`, avatar_url: '', open_id: `o-${id}`, active_status: 1,
    is_resigned: false, local_user_id: null, is_active: true,
    departments: [{ id: 1, name: '研发部', is_primary: true }],
  } as unknown as DirectoryUser);

  const POLICY_ASSIGNED = {
    application_id: 7,
    access_mode: 'assigned' as const,
    departments: [],
    users: [{ directory_user_id: 999, name: '已授权人员', avatar_url: '', departments: ['财务部'] }],
  };

  beforeEach(() => {
    vi.mocked(enterpriseApi.users).mockReset();
    vi.mocked(enterpriseApi.access).mockReset().mockResolvedValue(POLICY_ASSIGNED);
    pageState.items = [];
    pageState.error = null;
    pageState.hasMore = false;
  });

  it('popup scroll loads the next cursor page — the 101st candidate becomes selectable', async () => {
    pageState.items = [row(7, 'A资源')];
    vi.mocked(enterpriseApi.users)
      .mockImplementationOnce(async () => ({
        results: Array.from({ length: 50 }, (_, i) => dirUser(i + 1)),
        next_cursor: 'c1',
      }))
      .mockImplementationOnce(async () => ({
        results: Array.from({ length: 51 }, (_, i) => dirUser(i + 51)),
        next_cursor: null,
      }));

    await mountPage();
    await click(document.querySelector('[data-open-cell="7"] button')!);
    await flush(20);

    const select = () => document.querySelector<HTMLElement>('[data-testid="user-select"]')!;
    const options = () => JSON.parse(select().dataset.options!) as number[];
    // 第一页 50 人已在候选；已授权但不在目录页里的 999 也有 label
    // （policy.users 合并进 options，不退化成 raw id）。
    expect(options()).toContain(999);
    expect(options()).toContain(50);
    expect(options()).not.toContain(101);

    // 下拉滚到底 → 拉下一 cursor 页 → 第 101 人也可选（旧 limit:100 截断点）。
    await click(document.querySelector('[data-testid="select-scroll"]')!);
    await flush(20);
    expect(vi.mocked(enterpriseApi.users)).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'c1' }),
    );
    expect(options()).toContain(101);
    expect(options()).toContain(999); // 已授权 label 不丢
  });

  it('the search term goes to the SERVER (q), not browser filtering', async () => {
    pageState.items = [row(7, 'A资源')];
    vi.mocked(enterpriseApi.users).mockImplementation(
      async () => ({ results: [dirUser(1)], next_cursor: null }),
    );

    await mountPage();
    await click(document.querySelector('[data-open-cell="7"] button')!);
    await flush(20);
    expect(vi.mocked(enterpriseApi.users)).toHaveBeenCalledTimes(1);

    await click(document.querySelector('[data-testid="select-search"]')!);
    await flush(350); // useDirectoryUsers 的 300ms 防抖
    expect(vi.mocked(enterpriseApi.users)).toHaveBeenLastCalledWith(
      expect.objectContaining({ q: '张伟' }),
    );
  });

  it('closing the drawer resets the search — reopening B starts from a clean q (P1)', async () => {
    pageState.items = [row(7, 'A资源'), row(8, 'B资源')];
    vi.mocked(enterpriseApi.users).mockImplementation(
      async () => ({ results: [dirUser(1)], next_cursor: null }),
    );

    await mountPage();
    // A 会话：搜索「张伟」上服务端。
    await click(document.querySelector('[data-open-cell="7"] button')!);
    await flush(20);
    await click(document.querySelector('[data-testid="select-search"]')!);
    await flush(350);
    expect(vi.mocked(enterpriseApi.users)).toHaveBeenLastCalledWith(
      expect.objectContaining({ q: '张伟' }),
    );

    // 关闭 A（userQuery 被会话重置清空）→ 防抖落地 → 打开 B。
    await click(document.querySelector('[data-testid="drawer-close"]')!);
    await flush(350);
    await click(document.querySelector('[data-open-cell="8"] button')!);
    await flush(350);

    // B 的候选首页请求不再携带 A 会话遗留的 q —— 输入框为空，数据也不
    // 被旧词过滤（否则下拉显示旧词的过滤子集而输入框为空，UI 与数据
    // 不一致）。
    const calls = vi.mocked(enterpriseApi.users).mock.calls;
    const lastArgs = calls[calls.length - 1]![0]!;
    expect(lastArgs.q).toBeUndefined();
  });
});
