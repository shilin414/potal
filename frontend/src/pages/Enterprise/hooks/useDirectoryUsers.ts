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
}: UseDirectoryUsersOptions) {
  const [items, setItems] = useState<DirectoryUser[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [debouncedQuery, setDebouncedQuery] = useState(query);
  useEffect(() => {
    if (query === debouncedQuery) return undefined;
    const timer = setTimeout(() => setDebouncedQuery(query), debounceMs);
    return () => clearTimeout(timer);
  }, [query, debouncedQuery, debounceMs]);

  const requestIdRef = useRef(0);
  const fetchFirstPage = useCallback(async () => {
    if (!enabled) return;
    const requestId = ++requestIdRef.current;
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

  useEffect(() => { void fetchFirstPage(); }, [fetchFirstPage]);

  const loadMore = useCallback(async () => {
    if (!enabled || !cursor || loadingMore || loading) return;
    const requestId = requestIdRef.current;
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
      if (requestIdRef.current === requestId) setLoadingMore(false);
    }
  }, [enabled, debouncedQuery, limit, includeInactive, cursor, loadingMore, loading]);

  return {
    items, hasMore, loading, loadingMore, error,
    loadMore, refresh: fetchFirstPage,
  };
}
