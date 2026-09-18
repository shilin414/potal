/**
 * MobileCatalogContent — page mode (智能体中心/应用中心, 开发执行报告 §77).
 *
 * Pins the page variant against the SAME rules the sheet tests pin for the
 * sheet variant: recent max 3 / search hides recent / category filter /
 * agent-app split / disabled rows never listed (§22/§24).
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
}));

vi.mock('@/services/runApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/services/runApi')>()),
  fetchApplicationPage: mocks.fetchApplicationPage,
  fetchWorkspaceBootstrap: mocks.fetchWorkspaceBootstrap,
  setApplicationFavorite: mocks.setApplicationFavorite,
}));

import MobileCatalogContent from '../MobileCatalogContent';
import type { V2Application } from '@/services/runApi';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';

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
async function mountTracked(node: React.ReactElement) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => { root.render(node); });
  await flush(40);
  mounted.push({ host, root });
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

const app = (over: Partial<V2Application> & { id: number; name: string }): V2Application => ({
  slug: `slug-${over.id}`, description: '', icon: '', kind: 'chat',
  runtime_type: 'agent', provider_key: 'aily', capabilities: {}, enabled: true,
  ...over,
});

const POOL: V2Application[] = [
  app({ id: 1, name: '财务助手', kind: 'chat', category_slug: 'finance', category_name: '财务' }),
  app({ id: 2, name: '看板应用', kind: 'dashboard', category_slug: 'data', category_name: '数据' }),
  app({ id: 3, name: 'IT助手', kind: 'chat', category_slug: 'it', category_name: 'IT' }),
  app({ id: 4, name: '条码查询', kind: 'page', category_slug: 'data', category_name: '数据' }),
  app({ id: 5, name: '停用的智能体', kind: 'chat', enabled: false }),
  app({ id: 6, name: '人力助手', kind: 'chat', category_slug: 'hr', category_name: '人力' }),
];

beforeEach(() => {
  mocks.fetchApplicationPage.mockReset().mockResolvedValue({
    items: [], next_cursor: '', has_more: false,
  });
  mocks.fetchWorkspaceBootstrap.mockReset().mockResolvedValue({
    default_application: null, favorites: [], frequent: [], recent: [],
    recommended: [], recent_fixed_apps: [],
    agent_categories: [], app_categories: [],
  });
  useWorkspaceBootstrapStore.getState().clear();
  useApplicationEntityStore.getState().clear();
});

describe('MobileCatalogContent, page mode', () => {
  it('renders search + category rail + rich rows, agents only (§12/§16)', async () => {
    const onSelect = vi.fn();
    await mountTracked(
      <MobileCatalogContent
        type="agent" mode="page" applications={POOL} recentIds={[]}
        onSelect={onSelect}
      />,
    );

    expect(document.querySelector('.mobile-console-search input')).toBeTruthy();
    expect(document.querySelector('.mobile-console-rail')).toBeTruthy();
    expect(document.querySelector('.mobile-console-rail')!.textContent)
      .toContain('全部');

    const titles = Array.from(document.querySelectorAll('.mobile-console-row__title'))
      .map((el) => el.textContent);
    expect(titles).toContain('财务助手');
    expect(titles).toContain('IT助手');
    expect(titles).not.toContain('看板应用');   // app split (§22)
    expect(titles).not.toContain('停用的智能体'); // disabled never listed (§22)

    // Rich row: meta carries 分类 · Provider (§14). P2-1: the interactive
    // surface is the main button inside the wrapper.
    const row = Array.from(document.querySelectorAll('.mobile-console-row'))
      .find((el) => el.textContent!.includes('财务助手'))!;
    expect(row.querySelector('.mobile-console-row__meta')!.textContent)
      .toBe('财务 · aily');

    await click(row.querySelector('.mobile-console-row__main')!);
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it('app mode lists only non-chat kinds with 分类 · 类型 meta (§19)', async () => {
    await mountTracked(
      <MobileCatalogContent
        type="app" mode="page" applications={POOL} recentIds={[]}
        onSelect={() => {}}
      />,
    );
    const titles = Array.from(document.querySelectorAll('.mobile-console-row__title'))
      .map((el) => el.textContent);
    expect(titles).toContain('看板应用');
    expect(titles).toContain('条码查询');
    expect(titles).not.toContain('财务助手');
  });

  it('caps 最近使用 at 3 and hides it while searching (§13/§22)', async () => {
    const withRecency = POOL.map((a, i) => (
      a.kind === 'chat' ? { ...a, last_used_at: `2026-09-1${6 - i}T00:00:00Z` } : a));
    await mountTracked(
      <MobileCatalogContent
        type="agent" mode="page" applications={withRecency} recentIds={[]}
        onSelect={() => {}}
      />,
    );

    const recentSection = Array.from(document.querySelectorAll('.mobile-console-section'))
      .find((el) => el.textContent!.includes('最近使用'))!;
    const recentRows = recentSection.querySelectorAll('.mobile-console-row');
    expect(recentRows.length).toBeLessThanOrEqual(3);
    // The freshest chat agent leads the recent section.
    expect(recentRows[0].textContent).toContain('财务助手');

    // Typing hides the recent section entirely (§5.4).
    const input = document.querySelector<HTMLInputElement>('.mobile-console-search input')!;
    await act(async () => { setNativeValue(input, 'IT'); });
    await flush(40);
    expect(document.body.textContent).not.toContain('最近使用');
    expect(Array.from(document.querySelectorAll('.mobile-console-row__title'))
      .map((el) => el.textContent)).toContain('IT助手');
  });

  it('tapping a category pill narrows the list (§16)', async () => {
    await mountTracked(
      <MobileCatalogContent
        type="agent" mode="page" applications={POOL} recentIds={[]}
        onSelect={() => {}}
      />,
    );
    const hrPill = Array.from(document.querySelectorAll('.mobile-console-rail__pill'))
      .find((el) => el.textContent === '人力')!;
    await click(hrPill);
    await flush(40);

    const titles = Array.from(document.querySelectorAll('.mobile-console-row__title'))
      .map((el) => el.textContent);
    expect(titles).toEqual(['人力助手']);
  });

  it('favorites render the star and toggle calls the shared endpoint (§15)', async () => {
    mocks.setApplicationFavorite.mockResolvedValue({ application_id: 1, is_favorite: true });
    await mountTracked(
      <MobileCatalogContent
        type="agent" mode="page" favorites
        applications={[app({ id: 1, name: '财务助手', is_favorite: false })]}
        recentIds={[]} onSelect={() => {}}
      />,
    );
    const star = document.querySelector('.mobile-console-row__star')!;
    expect(star.getAttribute('aria-label')).toBe('收藏：财务助手');
    await click(star);
    await flush(20);
    expect(mocks.setApplicationFavorite).toHaveBeenCalledWith(1, true);
  });
});

describe('MobileCatalogContent — data boundary hardening (二次复审 P1-3/P2-3)', () => {
  it('a legacy local pool never fires a server request (P2-3)', async () => {
    await mountTracked(
      <MobileCatalogContent
        type="agent" mode="page" applications={POOL} recentIds={[]}
        onSelect={() => {}}
      />,
    );
    // The test name must cover BOTH server surfaces: the paged catalog AND
    // the workspace bootstrap (P2-4) — a local pool derives categories and
    // recency from the pool itself.
    expect(mocks.fetchApplicationPage).not.toHaveBeenCalled();
    expect(mocks.fetchWorkspaceBootstrap).not.toHaveBeenCalled();
  });

  it('a first-page request failure is an error state, not an empty catalog (P1-3)', async () => {
    mocks.fetchApplicationPage.mockReset().mockRejectedValueOnce(
      new Error('network down'),
    );
    await mountTracked(
      <MobileCatalogContent type="agent" mode="page" recentIds={[]} onSelect={() => {}} />,
    );

    expect(document.body.textContent).toContain('加载失败');
    expect(document.body.textContent).not.toContain('暂无可用智能体');
    expect(document.body.textContent).not.toContain('请联系管理员');

    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '重新加载');
    expect(retry).toBeTruthy();
  });

  it('a failed search request is an error, not 没有找到 (P1-3)', async () => {
    mocks.fetchApplicationPage.mockReset().mockResolvedValue({
      items: [app({ id: 1, name: '财务助手' })], next_cursor: '', has_more: false,
    });
    const { root } = await mountTracked(
      <MobileCatalogContent type="agent" mode="page" recentIds={[]} onSelect={() => {}} />,
    );
    const input = document.querySelector<HTMLInputElement>('.mobile-console-search input')!;
    await act(async () => { setNativeValue(input, '财务'); });
    mocks.fetchApplicationPage.mockRejectedValueOnce(new Error('network down'));
    await flush(400);

    expect(document.body.textContent).toContain('加载失败');
    expect(document.body.textContent).not.toContain('没有找到');

    await act(async () => { root.unmount(); });
  });

  it('a failed loadMore keeps the already-rendered rows (P1-3)', async () => {
    mocks.fetchApplicationPage.mockReset()
      .mockResolvedValueOnce({
        items: [app({ id: 1, name: '财务助手' })],
        next_cursor: 'c1', has_more: true,
      })
      .mockRejectedValueOnce(new Error('network down'));
    await mountTracked(
      <MobileCatalogContent type="agent" mode="page" recentIds={[]} onSelect={() => {}} />,
    );
    expect(document.body.textContent).toContain('财务助手');

    const more = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载更多');
    expect(more).toBeTruthy();
    await click(more!);
    await flush(20);

    // The first page is still rendered; the failure surfaces as a retry.
    expect(document.body.textContent).toContain('财务助手');
    expect(document.body.textContent).toContain('加载失败，点击重试');
  });

  it('a failed loadMore renders exactly ONE retry CTA, never 加载更多 alongside (P2-5)', async () => {
    mocks.fetchApplicationPage.mockReset()
      .mockResolvedValueOnce({
        items: [app({ id: 1, name: '财务助手' })],
        next_cursor: 'c1', has_more: true,
      })
      .mockRejectedValueOnce(new Error('network down'));
    await mountTracked(
      <MobileCatalogContent type="agent" mode="page" recentIds={[]} onSelect={() => {}} />,
    );

    await click(Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '加载更多')!);
    await flush(20);

    const ctaTexts = Array.from(document.querySelectorAll('button'))
      .map((b) => b.textContent);
    expect(ctaTexts).toContain('加载失败，点击重试');
    expect(ctaTexts).not.toContain('加载更多');
    // Exactly one of the two CTAs exists in the whole page body.
    const ctaCount = ctaTexts.filter(
      (t) => t === '加载失败，点击重试' || t === '加载更多',
    ).length;
    expect(ctaCount).toBe(1);
  });
});
