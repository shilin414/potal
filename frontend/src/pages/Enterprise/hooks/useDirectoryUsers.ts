/**
 * useDirectoryUsers — server-paged directory users (二次复审 P1-4).
 *
 * enterpriseApi.users already answers cursor pagination (results + next_cursor)
 * with a hard server cap of 100 rows per page, but MobileDirectoryPage used to
 * read exactly ONE page — every employee past the first 100 was invisible.
 * This hook mirrors useApplicationPage's contract so the directory page can
 * offer the same 加载更多 UX:
 *
 *   · `query` is debounced 300 ms and sent as q (server-side search);
 *   · `loadMore` appends the next cursor page, deduplicating by id;
 *   · a request failure keeps the already-loaded rows (error only flips the
 *     indicator — the same rule useApplicationPage pins for its consumers).
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { enterpriseApi, type DirectoryUser } from '../enterpriseApi';

export interface UseDirectoryUsersOptions {
  /** Server-side search term (debounced). */
  query?: string;
  /** False suppresses all fetching (e.g. the users tab is not active). */
  enabled?: boolean;
  /** Page size (server caps at 100). */
  limit?: number;
  /** Debounce for query changes (default 300 ms). */
  debounceMs?: number;
  /**
   * Directory management wants resigned users too; ACL pickers should only
   * offer active employees (二次复审 D1). Defaults to true — the directory
   * page's existing contract. False omits include_inactive entirely, which
   * the backend defaults to false.
   */
  includeInactive?: boolean;
  /**
   * Picker 会话标识（五次复审 §37–§38）：授权 Drawer 换目标 / Picker
   * 关闭重开时传入新值。变化 = 新会话：立即作废在途请求与 loadMore、
   * 清空 items/cursor/error，并把 debouncedQuery 同步为当前 query
   * （不等防抖）—— 快速关闭重开的第一帧不再闪上一个会话的搜索词、
   * 结果或错误。
   */
  sessionKey?: string | number | null;
}

const dedupeById = (items: DirectoryUser[]): DirectoryUser[] => {
  const seen = new Set<number>();
  const out: DirectoryUser[] = [];
  for (const item of items) {
    if (seen.has(item.id)) continue;
    seen.add(item.id);
    out.push(item);
  }
  return out;
};

export function useDirectoryUsers({
  query = '',
  enabled = true,
  limit = 50,
  debounceMs = 300,
  includeInactive = true,
  sessionKey,
}: UseDirectoryUsersOptions) {
  const [items, setItems] = useState<DirectoryUser[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [debouncedQuery, setDebouncedQuery] = useState(query);
  useEffect(() => {
    // 关闭期间搜索词清空立即同步（五次复审 §39）：宿主在关闭时清空搜索框，
    // 等满 300ms 防抖会让快速重开的第一帧仍带旧词多发一次请求。
    if (!enabled) {
      setDebouncedQuery(query);
      return undefined;
    }
    if (query === debouncedQuery) return undefined;
    const timer = setTimeout(() => setDebouncedQuery(query), debounceMs);
    return () => clearTimeout(timer);
  }, [enabled, query, debouncedQuery, debounceMs]);

  const requestIdRef = useRef(0);
  // loadMore generation (四次复审 P1-2): a NEW first page immediately revokes
  // the previous result set's in-flight loadMore — its spinner is released
  // the moment the new request STARTS, and a stale `finally` can never clear
  // a NEWER loadMore's spinner. Only the newest loadMore owns the spinner.
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
    setCursor(null);
    setHasMore(false);
    setError(null);
    setLoadingMore(false);
    setDebouncedQuery(query);
    if (enabled) setLoading(true);
  }

  // An INACTIVE surface (the users tab switched away, the picker sheet
  // closed) must abandon whatever is in flight (三次复审 §43–§45): bumping the
  // request id invalidates a travelling response so it can no longer
  // land and repopulate a surface nobody is looking at (a reopen would
  // otherwise briefly flash the previous search's rows/error). The items
  // are deliberately KEPT — they are still real data, and a reopen
  // refreshes page one anyway.
  useEffect(() => {
    if (enabled) return;
    requestIdRef.current += 1;
    loadMoreSeqRef.current += 1;
    setLoading(false);
    setLoadingMore(false);
  }, [enabled]);

  const fetchFirstPage = useCallback(async () => {
    if (!enabled) return;
    const requestId = ++requestIdRef.current;
    // New result set → the previous set's in-flight loadMore is void NOW.
    loadMoreSeqRef.current += 1;
    setLoadingMore(false);
    setLoading(true);
    setError(null);
    try {
      const page = await enterpriseApi.users({
        q: debouncedQuery.trim() || undefined,
        // Backend default is false — only the directory wants the resigned.
        ...(includeInactive ? { include_inactive: true } : {}),
        limit,
      });
      if (requestIdRef.current !== requestId) return;
      setItems(dedupeById(page.results));
      setCursor(page.next_cursor ?? null);
      setHasMore(Boolean(page.next_cursor));
    } catch {
      if (requestIdRef.current !== requestId) return;
      setError('加载人员失败');
      // Match useApplicationPage: a failed search/first page leaves NO cursor
      // — the retry must go back to page one of the current query, not
      // continue an old cursor and silently skip the new first page.
      setCursor(null);
      setHasMore(false);
    } finally {
      if (requestIdRef.current === requestId) setLoading(false);
    }
  }, [enabled, debouncedQuery, limit, includeInactive]);

  useEffect(() => { void fetchFirstPage(); }, [fetchFirstPage, sessionKey]);

  const loadMore = useCallback(async () => {
    if (!enabled || !cursor || loadingMore || loading) return;
    const requestId = requestIdRef.current;
    const myLoadMore = ++loadMoreSeqRef.current;
    setLoadingMore(true);
    try {
      const page = await enterpriseApi.users({
        q: debouncedQuery.trim() || undefined,
        // Backend default is false — only the directory wants the resigned.
        ...(includeInactive ? { include_inactive: true } : {}),
        limit,
        cursor,
      });
      if (requestIdRef.current !== requestId) return;
      setItems((current) => dedupeById([...current, ...page.results]));
      setCursor(page.next_cursor ?? null);
      setHasMore(Boolean(page.next_cursor));
      // A successful retry clears the 加载更多失败 indicator (二次复审 P2-1):
      // consumers render a persistent error tail off `error`.
      setError(null);
    } catch {
      if (requestIdRef.current !== requestId) return;
      // 加载更多 keeps the already-rendered rows; the next click retries.
      setError('加载更多失败');
    } finally {
      // Only the NEWEST loadMore owns the spinner (四次复审 P1-2): a new
      // search already revoked this one and reset the spinner; a stale
      // finally must not clear a NEWER loadMore's spinner either.
      if (loadMoreSeqRef.current === myLoadMore) {
        setLoadingMore(false);
      }
    }
  }, [enabled, debouncedQuery, limit, includeInactive, cursor, loadingMore, loading]);

  return {
    items, hasMore, loading, loadingMore, error,
    loadMore, refresh: fetchFirstPage,
  };
}
