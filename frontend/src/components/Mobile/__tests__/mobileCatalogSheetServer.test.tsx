/**
 * MobileCatalogSheet — SERVER-PAGED mode (执行报告 §4/§5/§24, P0-R1).
 *
 * Why this file exists: the 256 tests that were green before this change all
 * mounted the sheet with `applications={CATALOG}`, which forces the component
 * into its LEGACY local-pool branch. Production runs the paged branch, and
 * that branch had two real defects nobody could see:
 *
 *   · the category rail and 最近使用 were derived from the rows of the CURRENT
 *     PAGE, so 财务/采购/法务 never appeared until scrolled to, a tapped
 *     category collapsed the rail to itself, and 最近使用 lost every agent
 *     that was not on page one;
 *   · the active application was injected into whatever list was showing, so
 *     it polluted search results and unrelated categories.
 *
 * These cases therefore mount the sheet the way PRODUCTION does — with no
 * `applications` prop at all, against a mocked paged endpoint and a mocked
 * workspace bootstrap. The rules they pin are the report's own acceptance
 * list (§24 "移动端 Picker").
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  fetchApplicationPage: vi.fn(),
  fetchWorkspaceBootstrap: vi.fn(),
  setApplicationFavorite: vi.fn(),
  resolveApplication: vi.fn(),
}));

vi.mock('@/services/runApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/services/runApi')>()),
  fetchApplicationPage: mocks.fetchApplicationPage,
  fetchWorkspaceBootstrap: mocks.fetchWorkspaceBootstrap,
  setApplicationFavorite: mocks.setApplicationFavorite,
  resolveApplication: mocks.resolveApplication,
}));

import MobileCatalogSheet from '../MobileCatalogSheet';
import type { ApplicationSummary, V2Application } from '@/services/runApi';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';

// ── jsdom shims antd needs ────────────────────────────────────────────
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

async function mount(node: React.ReactElement): Promise<{ host: HTMLElement; root: Root }> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => { root.render(node); });
  await flush(40);
  return { host, root };
}

const mounted: Array<{ host: HTMLElement; root: Root }> = [];
afterEach(async () => {
  while (mounted.length) {
    const { host, root } = mounted.pop()!;
    await act(async () => { root.unmount(); });
    host.remove();
  }
  document.body.innerHTML = '';
});

async function mountTracked(node: React.ReactElement) {
  const entry = await mount(node);
  mounted.push(entry);
  return entry;
}

const rowByText = (text: string): HTMLElement | undefined => (
  Array.from(document.querySelectorAll<HTMLElement>('.mobile-sheet__row'))
    .find((el) => (el.textContent || '').includes(text)));

const tabByText = (text: string): HTMLElement | undefined => (
  Array.from(document.querySelectorAll<HTMLElement>('.mobile-sheet__tab'))
    .find((el) => (el.textContent || '').includes(text)));

const moreButton = (): HTMLElement | undefined => (
  document.querySelector<HTMLElement>('.mobile-sheet__more-btn') || undefined);

// ── fixtures ──────────────────────────────────────────────────────────

const summary = (over: Partial<ApplicationSummary> & { id: number; name: string }): ApplicationSummary => ({
  slug: `slug-${over.id}`, description: '', icon: '', kind: 'chat', enabled: true,
  ...over,
});

const app = (over: Partial<V2Application> & { id: number; name: string }): V2Application => ({
  slug: `slug-${over.id}`, description: '', icon: '', kind: 'chat',
  runtime_type: 'agent', provider_key: 'feishu_aily', capabilities: {}, enabled: true,
  ...over,
});

/** Page one of the whole picker: two agents, neither of them 财务. */
const PAGE_ONE = [
  app({ id: 1099, name: '销售助手', category_slug: 'it', category_name: 'IT运维' }),
  app({ id: 3, name: '合同助手', category_slug: 'hr', category_name: 'HR' }),
];

/** The HR page: exactly the rows a tapped category returns. */
const HR_PAGE = [app({ id: 3, name: '合同助手', category_slug: 'hr', category_name: 'HR' })];

/** A recent agent that page one does NOT contain — the §4.2 regression. */
const RECENT_OFF_PAGE = summary({ id: 998, slug: 'old-one', name: '很久以前用过的' });

/** The agent currently in play, in a DIFFERENT category (§4.3). */
const ACTIVE = app({
  id: 501, slug: 'active-sales', name: '销售助手501', category_slug: 'it',
  category_name: 'IT运维',
});

const BOOTSTRAP = {
  default_application: null,
  favorites: [],
  frequent: [],
  recent: [RECENT_OFF_PAGE],
  recommended: [],
  recent_fixed_apps: [],
  // The rail describes the CATALOG, not page one: 财务 has rows the first page
  // simply does not carry.
  agent_categories: [
    { slug: 'hr', name: 'HR', count: 3 },
    { slug: 'it', name: 'IT运维', count: 1 },
    { slug: 'finance', name: '财务', count: 2 },
  ],
  app_categories: [{ slug: 'office', name: '办公', count: 2 }],
};

beforeEach(() => {
  mocks.fetchApplicationPage.mockReset().mockImplementation(
    async (options: { categorySlug?: string }) => ({
      items: options?.categorySlug === 'hr' ? HR_PAGE : PAGE_ONE,
      next_cursor: '',
      has_more: false,
    }));
  mocks.fetchWorkspaceBootstrap.mockReset().mockResolvedValue(BOOTSTRAP);
  mocks.setApplicationFavorite.mockReset().mockResolvedValue({
    application_id: 1, is_favorite: true,
  });
  mocks.resolveApplication.mockReset();
  useWorkspaceBootstrapStore.getState().clear();
  useApplicationEntityStore.getState().clear();
});

const noop = () => {};

// ── the report's own acceptance list (§24 移动端 Picker / §5) ──────────

describe('MobileCatalogSheet, server-paged mode', () => {
  it('shows every available category even when page one does not contain it', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    expect(tabByText('HR')).toBeTruthy();
    expect(tabByText('IT运维')).toBeTruthy();
    // The regression: 财务 has no row on page one, and used to be missing.
    expect(tabByText('财务')).toBeTruthy();
  });

  it('keeps the other tabs after one is tapped', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    await click(tabByText('HR')!);
    await flush(40);

    // The tapped category narrows the LIST, never the rail.
    expect(tabByText('HR')).toBeTruthy();
    expect(tabByText('IT运维')).toBeTruthy();
    expect(tabByText('财务')).toBeTruthy();
    expect(document.body.textContent).toContain('合同助手');
    expect(document.body.textContent).not.toContain('销售助手501');
  });

  it('sends the tapped category to the server', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    await click(tabByText('财务')!);
    await flush(40);

    expect(mocks.fetchApplicationPage).toHaveBeenLastCalledWith(
      expect.objectContaining({ categorySlug: 'finance', kind: 'chat' }));
  });

  it('shows 最近使用 for an application that is not on the fetched page', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    expect(document.body.textContent).toContain('最近使用');
    expect(rowByText('很久以前用过的')).toBeTruthy();
  });

  it('keeps 最近使用 when a category is selected', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    await click(tabByText('HR')!);
    await flush(40);

    // 最近使用 is independent of the list below it (§5.2/§6).
    expect(rowByText('很久以前用过的')).toBeTruthy();
  });

  it('resolves a local recent id through the entity cache', async () => {
    useApplicationEntityStore.getState().upsertManage(
      app({ id: 77, slug: 'local', name: '刚用过的智能体' }));

    await mountTracked(
      <MobileCatalogSheet open type="agent" recentIds={[77]}
        onClose={noop} onSelect={noop} />,
    );

    expect(rowByText('刚用过的智能体')).toBeTruthy();
  });

  it('does not inject the active application into a category it is not in', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" recentIds={[]}
        activeApplicationId={ACTIVE.id} activeApplication={ACTIVE}
        onClose={noop} onSelect={noop} />,
    );

    // Unfiltered: the active row is pinned so its ✓ is visible (§5.5).
    expect(rowByText('销售助手501')).toBeTruthy();

    await click(tabByText('HR')!);
    await flush(40);

    // Inside a category the injected row would be a lie: it is an IT agent.
    expect(rowByText('销售助手501')).toBeUndefined();
  });

  it('does not inject the active application into search results', async () => {
    // The SERVER owns the search: an unmatched q comes back empty, which is
    // what the sheet must render (no local fallback, no injected row).
    mocks.fetchApplicationPage.mockImplementation(async (
      options: { q?: string },
    ) => (options?.q
      ? { items: [], next_cursor: '', has_more: false }
      : { items: PAGE_ONE, next_cursor: '', has_more: false }));

    await mountTracked(
      <MobileCatalogSheet open type="agent" recentIds={[]}
        activeApplicationId={ACTIVE.id} activeApplication={ACTIVE}
        onClose={noop} onSelect={noop} />,
    );

    await click(Array.from(document.querySelectorAll('button'))
      .find((el) => el.getAttribute('aria-label') === '搜索')!);
    // antd's Input puts the className on a wrapper span; the real control is
    // the <input> inside it.
    const input = document.querySelector<HTMLInputElement>('.mobile-sheet__search input')!;
    await act(async () => { setNativeValue(input, '不存在的名字'); });
    await flush(400); // the hook debounces `q` by 300 ms

    // An unmatched search shows the empty state and NOTHING else (§15.3).
    expect(document.body.textContent).toContain('没有找到');
    expect(rowByText('销售助手501')).toBeUndefined();
    expect(rowByText('销售助手')).toBeUndefined();
    expect(mocks.fetchApplicationPage).toHaveBeenLastCalledWith(
      expect.objectContaining({ q: '不存在的名字' }));
  });

  it('keeps the rail when a category legitimately has no rows', async () => {
    // A narrowed request may come back empty. Claiming "暂无可用智能体 /
    // 请联系管理员配置" AND hiding the tabs would leave the user stuck in a
    // category they cannot leave.
    mocks.fetchApplicationPage.mockImplementation(async (
      options: { categorySlug?: string },
    ) => (options?.categorySlug
      ? { items: [], next_cursor: '', has_more: false }
      : { items: PAGE_ONE, next_cursor: '', has_more: false }));

    await mountTracked(
      <MobileCatalogSheet open type="agent" recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    await click(tabByText('财务')!);
    await flush(40);

    expect(document.body.textContent).toContain('该分类下暂无内容');
    expect(document.body.textContent).not.toContain('暂无可用智能体');
    expect(tabByText('HR')).toBeTruthy();

    // …and going back to 全部 recovers the rows.
    await click(tabByText('全部')!);
    await flush(40);
    expect(rowByText('合同助手')).toBeTruthy();
  });

  it('loads the next page through the server cursor', async () => {
    mocks.fetchApplicationPage.mockImplementation(async (
      options: { cursor?: string | null },
    ) => (options?.cursor
      ? { items: [app({ id: 60, name: '第二页智能体' })], next_cursor: '', has_more: false }
      : { items: PAGE_ONE, next_cursor: 'CUR-1', has_more: true }));

    await mountTracked(
      <MobileCatalogSheet open type="agent" recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    expect(moreButton()).toBeTruthy();
    await click(moreButton()!);
    await flush(40);

    // 加载更多 is a REAL request, not a browser-side slice (§24).
    expect(mocks.fetchApplicationPage).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'CUR-1' }));
    expect(rowByText('第二页智能体')).toBeTruthy();
    expect(moreButton()).toBeUndefined();
  });

  it('issues no request while the sheet is closed', async () => {
    const { root } = await mountTracked(
      <MobileCatalogSheet open={false} type="agent" recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    expect(mocks.fetchApplicationPage).not.toHaveBeenCalled();
    expect(mocks.fetchWorkspaceBootstrap).not.toHaveBeenCalled();

    // …and exactly one first page when it opens.
    await act(async () => {
      root.render(
        <MobileCatalogSheet open type="agent" recentIds={[]}
          onClose={noop} onSelect={noop} />,
      );
    });
    await flush(40);

    expect(mocks.fetchApplicationPage).toHaveBeenCalledTimes(1);
    expect(mocks.fetchWorkspaceBootstrap).toHaveBeenCalled();
  });
});
