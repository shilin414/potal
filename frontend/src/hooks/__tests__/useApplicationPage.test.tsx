/**
 * useApplicationPage — server-paged catalog hook (执行报告 §22–§24).
 *
 * renderHook-style coverage via the repo's own createRoot/act harness (no
 * testing-library in this project): cursor append + dedupe, has_more tail,
 * filter reset to page one, per-item local patch, and the enabled gate
 * (closed mobile sheet must not fetch).
 * @vitest-environment jsdom
 */
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';
import { useApplicationPage } from '../useApplicationPage';
import { fetchApplicationPage } from '@/services/runApi';
import type { ApplicationPage, V2Application } from '@/services/runApi';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';

vi.mock('@/services/runApi', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  fetchApplicationPage: vi.fn(),
}));

const mockPage = vi.mocked(fetchApplicationPage);

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const app = (id: number, name = `agent-${id}`): V2Application => ({
  id, slug: `slug-${id}`, name, description: '', icon: '🤖', kind: 'chat',
  runtime_type: '', provider_key: '',  capabilities: {},
});

const page = (ids: number[], next: string, hasMore: boolean): ApplicationPage => ({
  // Explicit arrow: `ids.map(app)` would feed the array index into app's
  // optional `name: string` parameter.
  items: ids.map((id) => app(id)), next_cursor: next, has_more: hasMore,
});

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

interface Probe<T> { current: T | null }

function mountProbe(options: Record<string, unknown>, latest: Probe<ReturnType<typeof useApplicationPage>>) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  function Probe() {
    latest.current = useApplicationPage(options as unknown as Parameters<typeof useApplicationPage>[0]);
    return null;
  }
  return { host, root, element: React.createElement(Probe) };
}

let hosts: HTMLElement[];
let roots: Root[];

beforeEach(() => {
  mockPage.mockReset();
  useApplicationEntityStore.getState().clear();
  hosts = [];
  roots = [];
});

afterEach(() => {
  roots.forEach((root) => act(() => root.unmount()));
  hosts.forEach((host) => host.remove());
});

describe('useApplicationPage', () => {
  it('fetches page one on mount and exposes cursor state', async () => {
    mockPage.mockResolvedValue(page([1, 2], 'cursor-1', true));
    const latest: Probe<ReturnType<typeof useApplicationPage>> = { current: null };
    const { root, element } = mountProbe({ kind: 'chat' }, latest);
    roots.push(root);
    await act(async () => { root.render(element); });
    await flush(10);

    expect(latest.current!.items.map((item) => item.id)).toEqual([1, 2]);
    expect(latest.current!.hasMore).toBe(true);
    expect(mockPage).toHaveBeenCalledWith(expect.objectContaining({
      kind: 'chat', limit: 24,
    }));
    // The FIRST request carries no cursor.
    expect(mockPage.mock.calls[0][0]?.cursor).toBeUndefined();
  });

  it('atomically refreshes the shared entity cache for consume pages', async () => {
    useApplicationEntityStore.getState().upsertManage(app(1, 'old'));
    mockPage.mockResolvedValue({
      items: [app(1, 'new')], next_cursor: '', has_more: false,
    });
    const latest: Probe<ReturnType<typeof useApplicationPage>> = { current: null };
    const { root, element } = mountProbe({ kind: 'chat', mode: 'consume' }, latest);
    roots.push(root);
    await act(async () => { root.render(element); });
    await flush(10);

    expect(useApplicationEntityStore.getState().get(1)?.name).toBe('new');
    expect(useApplicationEntityStore.getState().validatedAtById[1])
      .toEqual(expect.any(Number));
  });

  it('appends the next page deduplicated and stops at the tail', async () => {
    mockPage
      .mockResolvedValueOnce(page([1, 2], 'cursor-1', true))
      .mockResolvedValueOnce(page([2, 3], '', false));
    const latest: Probe<ReturnType<typeof useApplicationPage>> = { current: null };
    const { root, element } = mountProbe({ kind: 'chat' }, latest);
    roots.push(root);
    await act(async () => { root.render(element); });
    await flush(10);

    await act(async () => { await latest.current!.loadMore(); });
    // id=2 arrived on both pages — it must render exactly once.
    expect(latest.current!.items.map((item) => item.id)).toEqual([1, 2, 3]);
    expect(latest.current!.hasMore).toBe(false);

    // No further traffic once has_more is false.
    await act(async () => { await latest.current!.loadMore(); });
    expect(mockPage).toHaveBeenCalledTimes(2);
  });

  it('resets to page one when the search changes', async () => {
    mockPage.mockResolvedValue(page([1, 2, 3], 'cursor-1', true));
    const latest: Probe<ReturnType<typeof useApplicationPage>> = { current: null };
    const host = document.createElement('div');
    hosts.push(host);
    const root = createRoot(host);
    roots.push(root);
    function Probe({ query }: { query?: string }) {
      latest.current = useApplicationPage({ kind: 'chat', query });
      return null;
    }
    await act(async () => { root.render(React.createElement(Probe, { query: undefined })); });
    await flush(10);
    expect(latest.current!.items).toHaveLength(3);

    mockPage.mockResolvedValueOnce(page([7], '', false));
    await act(async () => { root.render(React.createElement(Probe, { query: 'new' })); });
    // The hook debounces TYPING by 300 ms before refetching.
    await flush(350);
    expect(latest.current!.items.map((item) => item.id)).toEqual([7]);
    const last = mockPage.mock.calls[mockPage.mock.calls.length - 1][0];
    expect(last?.q).toBe('new');
    expect(last?.cursor).toBeUndefined();
  });

  it('sends kind/scope/category/unbound to the backend', async () => {
    mockPage.mockResolvedValue(page([], '', false));
    const latest: Probe<ReturnType<typeof useApplicationPage>> = { current: null };
    const { root, element } = mountProbe({
      kind: 'fixed', scope: 'manage', includeUnbound: true, category: 'forms',
    }, latest);
    roots.push(root);
    await act(async () => { root.render(element); });
    await flush(10);

    expect(mockPage).toHaveBeenCalledWith(expect.objectContaining({
      kind: 'fixed', scope: 'manage', includeUnbound: true, categorySlug: 'forms',
    }));
  });

  it('patches one item locally without any refetch', async () => {
    mockPage.mockResolvedValue(page([1, 2], '', false));
    const latest: Probe<ReturnType<typeof useApplicationPage>> = { current: null };
    const { root, element } = mountProbe({ kind: 'fixed' }, latest);
    roots.push(root);
    await act(async () => { root.render(element); });
    await flush(10);
    const calls = mockPage.mock.calls.length;

    act(() => { latest.current!.patchItem(2, { enabled: false }); });
    expect(latest.current!.items.find((item) => item.id === 2)?.enabled).toBe(false);
    expect(latest.current!.items.find((item) => item.id === 1)?.enabled).toBeUndefined();
    expect(mockPage).toHaveBeenCalledTimes(calls);
  });

  it('never fetches while enabled=false (closed mobile drawer)', async () => {
    const latest: Probe<ReturnType<typeof useApplicationPage>> = { current: null };
    const host = document.createElement('div');
    hosts.push(host);
    const root = createRoot(host);
    roots.push(root);
    function Probe({ enabled }: { enabled: boolean }) {
      latest.current = useApplicationPage({ kind: 'chat', enabled });
      return null;
    }
    await act(async () => { root.render(React.createElement(Probe, { enabled: false })); });
    await flush(10);
    expect(mockPage).not.toHaveBeenCalled();

    mockPage.mockResolvedValue(page([1], '', false));
    await act(async () => { root.render(React.createElement(Probe, { enabled: true })); });
    await flush(10);
    expect(latest.current!.items.map((item) => item.id)).toEqual([1]);
  });

  it('keeps the previous rows when a page request fails', async () => {
    mockPage.mockResolvedValue(page([1, 2], 'cursor-1', true));
    const latest: Probe<ReturnType<typeof useApplicationPage>> = { current: null };
    const { root, element } = mountProbe({ kind: 'chat' }, latest);
    roots.push(root);
    await act(async () => { root.render(element); });
    await flush(10);

    mockPage.mockRejectedValueOnce(new Error('network down'));
    await act(async () => { await latest.current!.loadMore(); });
    expect(latest.current!.items.map((item) => item.id)).toEqual([1, 2]);
    // cursor / has_more survive a failed loadMore, so the next click retries.
    expect(latest.current!.hasMore).toBe(true);
    expect(latest.current!.error).toBeTruthy();
  });

  it('a NEW result set releases the stale loadMore spinner immediately; the new set pages on its own (四次复审 P1-2)', async () => {
    // A 的 loadMore 在途时新搜索开始：旧请求必须当场作废（spinner 立即
    // false，而不是等旧 HTTP 结束），新结果集能立刻翻自己的下一页；旧请求
    // 晚到时既不能覆盖新数据，也不能关掉新 loadMore 的 spinner。
    let resolveStale!: (value: ApplicationPage) => void;
    let resolveB!: (value: ApplicationPage) => void;
    mockPage
      .mockResolvedValueOnce(page([1, 2], 'cursor-1', true))
      .mockImplementationOnce(() => new Promise<ApplicationPage>((res) => {
        resolveStale = res;
      }))
      .mockResolvedValueOnce(page([9], 'cursor-9', true))
      .mockImplementationOnce(() => new Promise<ApplicationPage>((res) => {
        resolveB = res;
      }));
    const latest: Probe<ReturnType<typeof useApplicationPage>> = { current: null };
    const host = document.createElement('div');
    hosts.push(host);
    const root = createRoot(host);
    roots.push(root);
    function Probe({ query }: { query?: string }) {
      latest.current = useApplicationPage({ kind: 'chat', query });
      return null;
    }
    await act(async () => { root.render(React.createElement(Probe, { query: undefined })); });
    await flush(10);
    expect(latest.current!.hasMore).toBe(true);

    const stale = latest.current!.loadMore();
    await flush(10);
    expect(latest.current!.loadingMore).toBe(true);

    // 新搜索在 loadMore 在途时开始：bump request id + 新首页。
    await act(async () => { root.render(React.createElement(Probe, { query: '新词' })); });
    await flush(350); // 300ms 防抖到期 + 新首页落地
    expect(latest.current!.loading).toBe(false);
    expect(latest.current!.loadingMore).toBe(false); // 旧 loadMore 已当场作废

    // 新结果集可以立即翻自己的下一页（旧请求仍在途）。
    const bMore = latest.current!.loadMore();
    await flush(10);
    expect(mockPage).toHaveBeenLastCalledWith(expect.objectContaining({
      q: '新词', cursor: 'cursor-9',
    }));
    expect(latest.current!.loadingMore).toBe(true);  // 新结果集自己的 spinner

    // 旧响应此刻才返回：不得覆盖新数据、不得关掉新 spinner。
    await act(async () => { resolveStale(page([3, 4], '', false)); });
    await act(async () => { await stale; });
    await flush(10);
    expect(latest.current!.items.map((item) => item.id)).toEqual([9]);
    expect(latest.current!.loadingMore).toBe(true);  // 新 spinner 仍在
    expect(latest.current!.error).toBeNull();

    // 新结果集的第二页落地，spinner 才复位。
    await act(async () => { resolveB(page([10], '', false)); });
    await act(async () => { await bMore; });
    await flush(10);
    expect(latest.current!.loadingMore).toBe(false);
    expect(latest.current!.items.map((item) => item.id)).toEqual([9, 10]);
  });
});
