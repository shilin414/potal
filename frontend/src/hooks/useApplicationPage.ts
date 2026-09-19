/**
 * useApplicationPage — ONE server-paged catalog hook (执行报告 §22–§24).
 *
 * The smart-agent market, the app center and the mobile pickers used to each
 * roll their own browser-side filtering over the WHOLE catalog
 * (`GET /v2/applications` has no server-side cap; the shared dev DB once
 * carried 1800+ rows). This hook replaces those copies with the real paged
 * endpoint:
 *
 *   · the server evaluates LIMIT + the cursor condition in SQL;
 *   · `q` / `category` are sent to the backend (300 ms debounced), so search
 *     covers the entire catalog instead of the pages already loaded;
 *   · a filter change resets to page one — keeping a grown cursor across a
 *     narrowed result set would skip past what the user is looking at;
 *   · a loadMore invalidated by a NEW result set is void the moment the new
 *     first page starts (loadMore generation, 四次复审 P1-2) — the stale
 *     request's late finally can neither keep the new set's 加载更多 spinner
 *     on nor clear a newer loadMore's spinner;
 *   · `loadMore` appends the next cursor page, deduplicating by id so an
 *     application edited between page fetches cannot render twice;
 *   · `patchItem` applies a local update (favourite / enabled / public
 *     toggles) without re-downloading anything (执行报告 §31).
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import {
  fetchApplicationPage,
  type V2Application,
} from '@/services/runApi';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';

export interface UseApplicationPageOptions {
  kind: 'chat' | 'fixed' | 'all';
  scope?: 'accessible' | 'public' | 'manage' | 'mine';
  /**
   * `manage` (default) lists what the caller may ADMINISTER — including
   * disabled, private and binding-less rows, which is what 智能体市场 and
   * 应用中心 need in order to repair them.
   *
   * `consume` (二次复审 P0-5) lists what the caller may actually OPEN or
   * RUN: enabled, and bound when `kind === 'chat'`. Every picker, switcher
   * and shortcut list must use it — otherwise a staff user sees (and can
   * click) a 停用 agent whose first message the run API refuses.
   */
  mode?: 'manage' | 'consume';
  includeUnbound?: boolean;
  /** Client-side category filter, sent to the backend as category_slug. */
  category?: string | null;
  /** Client-side search, sent to the backend as q (debounced). */
  query?: string;
  /** Page size (default 24 — the same budget the market page renders). */
  limit?: number;
  /** Debounce for query changes (default 300 ms). */
  debounceMs?: number;
  /**
   * False suppresses ALL fetching (e.g. a mobile sheet still mounted inside
   * a closed Drawer). Turning it true triggers the first page.
   */
  enabled?: boolean;
  /**
   * Picker 会话标识（五次复审 §37–§38）：remote-search Picker 的宿主
   * Surface 换会话（关闭重开 / 换编辑对象 / 换授权目标）时传入新值。
   * 变化 = 新会话：立即作废在途请求与 loadMore、清空 items/cursor/
   * error，并把 debouncedQuery 同步为当前 query（不等防抖）—— 快速
   * 关闭重开的第一帧不再闪上一个会话的搜索词、结果或错误。
   */
  sessionKey?: string | number | null;
}


const dedupeById = (items: V2Application[]): V2Application[] => {
  const seen = new Set<number>();
  const out: V2Application[] = [];
  for (const item of items) {
    if (seen.has(item.id)) continue;
    seen.add(item.id);
    out.push(item);
  }
  return out;
};

export function useApplicationPage(options: UseApplicationPageOptions) {
  const {
    kind, scope, mode, includeUnbound, category, query, limit = 24,
    debounceMs = 300, enabled = true, sessionKey,
  } = options;

  const [items, setItems] = useState<V2Application[]>([]);
  const [cursor, setCursor] = useState('');
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The debounce only smooths TYPING: category switches and the initial
  // mount fire immediately (a tab switch must not lag 300 ms). While the
  // surface is CLOSED the query is synced immediately (五次复审 §39) — the
  // host clears its search box on close, and waiting out the debounce would
  // let a quick reopen fire one more request with the stale term.
  const [debouncedQuery, setDebouncedQuery] = useState(query ?? '');
  useEffect(() => {
    if (!enabled) {
      setDebouncedQuery(query ?? '');
      return undefined;
    }
    const next = query ?? '';
    if (next === debouncedQuery) return undefined;
    const timer = setTimeout(() => setDebouncedQuery(next), debounceMs);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, query, debounceMs]);

  // A new filter combination is a NEW result set: reset to page one. The
  // request id guards against out-of-order responses when the user types
  // faster than the network answers.
  const requestIdRef = useRef(0);
  // loadMore generation (四次复审 P1-2): result-set id and PAGING operation id
  // are two different generations. A new first page immediately revokes the
  // old result set's loadMore — its spinner is released the moment the new
  // request STARTS, not when the stale HTTP finally settles (a wedged 60s
  // request must not block the new filter's 加载更多). The loadMore seq also
  // keeps a late `finally` from an OLD loadMore clearing the CURRENT one's
  // spinner: only the newest loadMore owns the spinner state.
  const loadMoreSeqRef = useRef(0);

  // Picker 会话切换的 render-time 重置（五次复审 §37–§38）：sessionKey 变化
  // 的那一帧（而不是 passive effect 之后）就作废在途请求、清空会话状态，
  // 并把 debouncedQuery 同步为当前 query —— 同一次 commit 的首屏请求因此
  // 直接携带正确的（通常是已清空的）搜索词，不会先漏发一次旧词请求。
  // render-time setState 是 React 官方的「props 变化时调整 state」模式：
  // React 会立刻用新 state 重渲染再提交，effect 里看到的已是重置后的值。
  const prevSessionRef = useRef(sessionKey);
  if (prevSessionRef.current !== sessionKey) {
    prevSessionRef.current = sessionKey;
    requestIdRef.current += 1;
    loadMoreSeqRef.current += 1;
    setItems([]);
    setCursor('');
    setHasMore(false);
    setError(null);
    setLoadingMore(false);
    setDebouncedQuery(query ?? '');
    if (enabled) setLoading(true);
  }

  const fetchFirstPage = useCallback(async () => {
    if (!enabled) return; // closed sheet / gated surface — no traffic
    const requestId = ++requestIdRef.current;
    // New result set → the previous set's in-flight loadMore is void NOW.
    loadMoreSeqRef.current += 1;
    setLoadingMore(false);
    setLoading(true);
    setError(null);
    try {
      const page = await fetchApplicationPage({
        kind,
        scope,
        mode,
        includeUnbound,
        q: debouncedQuery,
        categorySlug: category || undefined,
        limit,
      });
      if (requestIdRef.current !== requestId) return; // a newer query won
      const nextItems = dedupeById(page.items);
      setItems(nextItems);
      // A consume page returns complete entities: write the snapshot and
      // its consume trust stamp atomically so freshness can never bless older
      // data already present in the shared entity cache.
      if (mode === 'consume') {
        useApplicationEntityStore.getState().upsertManyConsume(nextItems);
      }
      setCursor(page.next_cursor);
      setHasMore(page.has_more);
    } catch (err: any) {
      if (requestIdRef.current !== requestId) return;
      // An error leaves the previous items in place (they are still real
      // data); only the indicator flips, and 加载更多 is suppressed.
      setError(err?.response?.data?.detail || '加载列表失败');
      setCursor('');
      setHasMore(false);
    } finally {
      if (requestIdRef.current === requestId) setLoading(false);
    }
  }, [enabled, kind, scope, mode, includeUnbound, debouncedQuery, category, limit]);

  useEffect(() => { void fetchFirstPage(); }, [fetchFirstPage, sessionKey]);

  // A surface that goes INACTIVE (a closed Drawer, a hidden sheet) must also
  // abandon whatever is in flight (执行报告 §22 / P2-4): `enabled: false` used
  // to suppress only NEW requests, so a response that was already travelling
  // could still land and repopulate a surface nobody is looking at (and a
  // later reopen would briefly show it). Bumping the request id invalidates
  // them, and clearing the spinners keeps the next open from looking stuck.
  // The items are deliberately KEPT: they are still real data, and a reopen
  // refreshes page one anyway.
  useEffect(() => {
    if (enabled) return;
    requestIdRef.current += 1;
    loadMoreSeqRef.current += 1;
    setLoading(false);
    setLoadingMore(false);
  }, [enabled]);

  const loadMore = useCallback(async () => {
    if (!enabled || !cursor || loadingMore || loading) return;
    const requestId = requestIdRef.current;
    const myLoadMore = ++loadMoreSeqRef.current;
    setLoadingMore(true);
    try {
      const page = await fetchApplicationPage({
        kind, scope, mode, includeUnbound, q: debouncedQuery,
        categorySlug: category || undefined, limit, cursor,
      });
      if (requestIdRef.current !== requestId) return; // filters changed mid-flight
      if (mode === 'consume') {
        useApplicationEntityStore.getState().upsertManyConsume(page.items);
      }
      setItems((current) => dedupeById([...current, ...page.items]));
      setCursor(page.next_cursor);
      setHasMore(page.has_more);
      // A successful retry clears the 加载更多失败 indicator (二次复审 P2):
      // consumers render a persistent partial-error tail off `error`.
      setError(null);
    } catch (err: any) {
      if (requestIdRef.current !== requestId) return; // filters changed mid-flight
      // 加载更多 keeps the already-rendered pages; the next click retries.
      setError(err?.response?.data?.detail || '加载更多失败');
    } finally {
      // Only the NEWEST loadMore owns the spinner: a new first page already
      // revoked this one (and reset the spinner) — a stale finally must not
      // clear a NEWER loadMore's spinner either (四次复审 P1-2).
      if (loadMoreSeqRef.current === myLoadMore) {
        setLoadingMore(false);
      }
    }
  }, [enabled, kind, scope, mode, includeUnbound, debouncedQuery, category, limit, cursor, loadingMore, loading]);

  /** Local update of ONE card (执行报告 §31 — never a full reload). */
  const patchItem = useCallback((id: number, patch: Partial<V2Application>) => {
    setItems((current) => current.map((item) => (
      item.id === id ? { ...item, ...patch } : item)));
  }, []);

  /** Replace one item wholesale (e.g. after an avatar upload). */
  const replaceItem = useCallback((id: number, next: V2Application) => {
    setItems((current) => current.map((item) => (
      item.id === id ? { ...next, id } : item)));
  }, []);

  return {
    items, hasMore, loading, loadingMore, error,
    loadMore, refresh: fetchFirstPage, patchItem, replaceItem,
  };
}
