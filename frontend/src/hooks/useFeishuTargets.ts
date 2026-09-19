/**
 * useFeishuTargets — 飞书目标的远程搜索（五次复审 P1-3 / P2-7 / P2-8，
 * 六次复审 P1-3 / P2-1 / P2-3）。
 *
 * Schedule Editor 的投递目标与 FeishuForwardModal 共享本 hook：
 *   · user（目录搜索）：空 query 不发请求（idle —— UI 提示输入姓名搜索），
 *     空查询只会拿到「全公司任意前 20 人」；有 query 才服务端搜索，并走
 *     cursor 分页（search/v1/user 官方 page_token 续拉）—— 第 51 个之后
 *     的匹配靠 loadMore 续拉，不再停在第一页；
 *   · chat：一个 picker 会话内只请求一次全量（六次复审 P2-1），query 由
 *     本 hook 在完整数据集上本地过滤 —— 旧实现每输入一个字符就触发一次
 *     后端全量分页扫描（运/运营/运营群 各扫一遍）。会话内缓存随
 *     sessionKey 生命周期存续：切 tab / 关再开投递不重拉，弹窗关闭即作废；
 *   · 请求代际（request/loadMore generation，六次复审 §十九）：只有最新
 *     query 的响应可以落地，旧 query 迟到的续拉页不得 append 到新结果；
 *   · ERROR ≠ EMPTY：失败暴露 error + errorStatus，refresh() 重试；
 *   · status（六次复审 P2-3）：idle（未参与查询）≠ success(0 条)——
 *     Schedule Editor 据此区分「部分数据源失败」与「全部参与的数据源都
 *     失败」，空 query 下 chat 失败不再是「部分失败」而是全失败；
 *   · 分页错误与首页错误分离（七次复审 P1-2/P1-3）：loadMore 失败只置
 *     loadMoreError（status 保持 success、已加载页保留、hasMore 保留），
 *     绝不把「第 4 页拉挂了」升级成「整个数据源失败」把前 150 条一起
 *     清掉；续拉成功必须清掉 loadMoreError（旧实现 page2 重试成功后
 *     error 仍残留，UI 永远显示失败态）；
 *   · Surface 关闭（enabled=false）或 sessionKey 变化：立即作废在途
 *     请求并清空会话状态 —— 快速重开的第一帧不闪旧结果/旧错误。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { fetchFeishuTargets, type FeishuForwardTarget } from '@/services/shareApi';

export type FeishuTargetType = 'user' | 'chat';

/** 数据源生命周期（六次复审 P2-3）：未请求 ≠ 请求成功但 0 条。 */
export type FeishuTargetStatus = 'idle' | 'loading' | 'success' | 'error';

export interface UseFeishuTargetsOptions {
  type: FeishuTargetType;
  /** Surface 是否打开（编辑器打开且开启投递 / 转发弹窗的当前 tab）。 */
  enabled: boolean;
  /** 搜索词（hook 内部防抖后直发后端；chat 仅用于本地过滤）。 */
  query: string;
  /** 搜索防抖（默认 300ms）。 */
  debounceMs?: number;
  /**
   * Picker 会话标识（五次复审 §37–§38）：变化 = 新会话，立即作废在途
   * 请求并清空 items/error（含 chat 会话缓存），且 debouncedQuery 同步
   * 为当前 query。
   */
  sessionKey?: string | number | null;
}

export interface UseFeishuTargetsResult {
  /** 当前应展示的目标（chat = 缓存全量按 query 本地过滤；user = 已累积页）。 */
  items: FeishuForwardTarget[];
  loading: boolean;
  /** 续拉下一页（仅 user；chat 恒无更多）。 */
  loadingMore: boolean;
  /** 失败信息（ERROR ≠ EMPTY —— 绝不静默成空列表）。 */
  error: string | null;
  /** 失败响应的 HTTP 状态码（403/400 = 需要重新授权飞书）。 */
  errorStatus: number | null;
  /**
   * 续拉下一页失败（七次复审 P1-2）：partial paging error，不是整个数据
   * 源失败 —— status/items/hasMore 均保持原状，Host 在列表底部给「更多
   * 加载失败 + 重试续拉」入口即可，不得清掉已加载页。
   */
  loadMoreError: string | null;
  /** 续拉失败的 HTTP 状态码（403 = 授权变化，Host 仍要给重新授权入口）。 */
  loadMoreErrorStatus: number | null;
  /** 是否还有下一页（仅 user 分页有意义）。 */
  hasMore: boolean;
  /** idle = 未参与查询（如 user 空 query），区别于 success(0 条)。 */
  status: FeishuTargetStatus;
  /** 续拉下一页并 append（去重）。 */
  loadMore: () => Promise<void>;
  /** 按当前 query 重新请求（重试入口；chat 强制重拉全量）。 */
  refresh: () => Promise<void>;
}

export function useFeishuTargets({
  type, enabled, query, debounceMs = 300, sessionKey,
}: UseFeishuTargetsOptions): UseFeishuTargetsResult {
  // serverItems：user = 已累积的页；chat = 会话内全量缓存。
  const [serverItems, setServerItems] = useState<FeishuForwardTarget[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [errorStatus, setErrorStatus] = useState<number | null>(null);
  // 分页错误独立于首页错误（七次复审 P1-2）：loadMore 失败不动 status/
  // items/hasMore —— 已加载的页仍然有效，只有「下一页」暂时拉不到。
  const [loadMoreError, setLoadMoreError] = useState<string | null>(null);
  const [loadMoreErrorStatus, setLoadMoreErrorStatus] = useState<number | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [status, setStatus] = useState<FeishuTargetStatus>('idle');

  // 请求代际（五次复审 §29 + 六次复审 §十九）：requestSeq = first-page
  // 代际（query/session/refresh 变化 +1），loadMoreSeq = 续拉代际。旧
  // query 迟到的续拉页两个序号都对不上，整体丢弃 —— 不得 append 到新
  // query 的结果。
  const requestSeqRef = useRef(0);
  const loadMoreSeqRef = useRef(0);
  // user 下一页游标（provider page_token）。
  const cursorRef = useRef('');
  // chat 会话缓存（六次复审 P2-1）：session 内只拉一次全量。ref 存活于
  // enabled 切换之外 —— 切 tab / 关开投递开关不重拉；sessionKey 变化
  // （弹窗关闭重开）才作废。
  const chatCacheRef = useRef<FeishuForwardTarget[] | null>(null);

  const [debouncedQuery, setDebouncedQuery] = useState(query);
  useEffect(() => {
    // 关闭期间 / 搜索词被清空：立即同步（§39）—— 空 query 是「无搜索」
    // 状态而不是一次输入，在途的旧词请求必须当场作废，不能在 300ms 窗口
    // 里先落地一次旧词结果（输入框已空而列表显示旧词匹配）。
    if (!enabled || !query.trim()) {
      setDebouncedQuery(query);
      return undefined;
    }
    if (query === debouncedQuery) return undefined;
    const timer = setTimeout(() => setDebouncedQuery(query), debounceMs);
    return () => clearTimeout(timer);
  }, [enabled, query, debouncedQuery, debounceMs]);

  const resetSessionState = useCallback((nextQuery: string) => {
    requestSeqRef.current += 1;
    loadMoreSeqRef.current += 1;
    cursorRef.current = '';
    chatCacheRef.current = null;
    setServerItems([]);
    setLoading(false);
    setLoadingMore(false);
    setError(null);
    setErrorStatus(null);
    setLoadMoreError(null);
    setLoadMoreErrorStatus(null);
    setHasMore(false);
    setStatus('idle');
    setDebouncedQuery(nextQuery);
  }, []);

  // 会话切换的 render-time 重置（§37–§38）：sessionKey 变化的那一帧就
  // 清空会话状态并同步 debouncedQuery —— 同一次 commit 里的首屏请求
  // 因此直接带上正确的（已清空的）query，而不是先发一次旧词请求。
  const prevSessionRef = useRef(sessionKey);
  if (prevSessionRef.current !== sessionKey) {
    prevSessionRef.current = sessionKey;
    resetSessionState(query);
  }

  // chat 的请求不依赖 query（过滤在本地完成）：把 chat 的 query 依赖固定
  // 为空串，输入词不再触发后端全量分页扫描（六次复审 P2-1 —— 旧实现每
  // 输入一个字符就重扫一遍完整群列表）。
  const effectiveQuery = type === 'user' ? debouncedQuery : '';

  const fetchFirstPage = useCallback(async () => {
    if (!enabled) return;
    // user 是目录搜索：空 query 不发请求 —— idle 而非 success(empty)
    // （六次复审 P2-3），空查询只会拿到「任意前 20 人」。
    if (type === 'user' && !effectiveQuery.trim()) {
      requestSeqRef.current += 1; // 作废在途搜索（用户把词删空了）
      loadMoreSeqRef.current += 1;
      cursorRef.current = '';
      // 作废在途请求后 loading/loadingMore 无人再清（finally 的 seq 已对
      // 不上），必须在这里显式复位，否则 UI 永远转圈。
      setLoading(false);
      setLoadingMore(false);
      setServerItems([]);
      setHasMore(false);
      setError(null);
      setErrorStatus(null);
      setLoadMoreError(null);
      setLoadMoreErrorStatus(null);
      setStatus('idle');
      return;
    }
    // chat 会话缓存命中（六次复审 P2-1）：切 tab / 关开投递开关恢复缓存，
    // 不再请求 —— query 过滤由下方 items memo 在完整数据集上完成。
    if (type === 'chat' && chatCacheRef.current) {
      requestSeqRef.current += 1; // 作废在途请求
      loadMoreSeqRef.current += 1;
      setLoading(false);
      setLoadingMore(false);
      setServerItems(chatCacheRef.current);
      setHasMore(false);
      setError(null);
      setErrorStatus(null);
      setLoadMoreError(null);
      setLoadMoreErrorStatus(null);
      setStatus('success');
      return;
    }
    const seq = ++requestSeqRef.current;
    loadMoreSeqRef.current += 1; // 新首页作废在途续拉
    // 作废在途续拉后其 finally 的双 seq 检查必不过、不会自己清
    // loadingMore —— 必须在此复位，否则「续拉在途时换搜索词」会把
    // loadingMore 永久卡在 true，锁死后续 loadMore / 滚动续拉。
    setLoadingMore(false);
    cursorRef.current = '';
    setLoading(true);
    setError(null);
    setErrorStatus(null);
    // 新的首页请求也作废旧的分页错误：这是新的数据代际，不是旧代际
    // 的第 2 页重试（七次复审 P1-2）。
    setLoadMoreError(null);
    setLoadMoreErrorStatus(null);
    setStatus('loading');
    try {
      // effectiveQuery：user = debouncedQuery（服务端搜索）；chat 固定为空串
      // → 不带 query 参数（全量拉取后本地过滤，六次复审 P2-1）。
      const page = await fetchFeishuTargets(type, effectiveQuery.trim() || undefined);
      if (seq !== requestSeqRef.current) return; // 旧查询晚到：整体丢弃
      const list = page.items ?? [];
      if (type === 'chat') chatCacheRef.current = list;
      setServerItems(list);
      cursorRef.current = page.next_cursor ?? '';
      setHasMore(Boolean(page.has_more) && (page.next_cursor ?? '') !== '');
      setStatus('success');
    } catch (e: any) {
      if (seq !== requestSeqRef.current) return;
      setError(
        e?.response?.data?.error
        || e?.response?.data?.detail
        || (type === 'user' ? '搜索联系人失败' : '获取群聊列表失败'),
      );
      setErrorStatus(e?.response?.status ?? null);
      setServerItems([]);
      setHasMore(false);
      setStatus('error');
    } finally {
      if (seq === requestSeqRef.current) setLoading(false);
    }
  }, [enabled, type, effectiveQuery]);

  useEffect(() => { void fetchFirstPage(); }, [fetchFirstPage, sessionKey]);

  // Surface 关闭：立即作废在途请求 + 清空 UI 状态（重开第一帧不闪旧数据）。
  // chat 会话缓存保留在 ref 里 —— session 未换代时切回可直接恢复。
  useEffect(() => {
    if (enabled) return;
    requestSeqRef.current += 1;
    loadMoreSeqRef.current += 1;
    setServerItems([]);
    setLoading(false);
    setLoadingMore(false);
    setError(null);
    setErrorStatus(null);
    setLoadMoreError(null);
    setLoadMoreErrorStatus(null);
    setHasMore(false);
    setStatus('idle');
  }, [enabled]);

  const loadMore = useCallback(async () => {
    // 续拉只属于 user 分页（chat 恒为全量单页）。
    if (!enabled || type !== 'user') return;
    if (!hasMore || loading || loadingMore) return;
    if (!debouncedQuery.trim()) return;
    const seq = requestSeqRef.current;
    const loadSeq = ++loadMoreSeqRef.current;
    setLoadingMore(true);
    try {
      const page = await fetchFeishuTargets(
        'user', debouncedQuery.trim(), cursorRef.current,
      );
      // 旧 query 的迟到页不得 append：query 已变 / 新首页已发 / 又一次
      // 续拉已发，任一发生即作废。
      if (seq !== requestSeqRef.current || loadSeq !== loadMoreSeqRef.current) return;
      setServerItems((prev) => {
        const seen = new Set(prev.map((t) => t.id));
        return [...prev, ...page.items.filter((t) => !seen.has(t.id))];
      });
      cursorRef.current = page.next_cursor ?? '';
      setHasMore(Boolean(page.has_more) && (page.next_cursor ?? '') !== '');
      // 续拉成功必须真正清除分页错误（七次复审 P1-3）：page2 第一次失败后
      // 重试成功，数据已 append 而 error 残留会让 UI 永远显示失败态。
      setLoadMoreError(null);
      setLoadMoreErrorStatus(null);
    } catch (e: any) {
      if (seq !== requestSeqRef.current || loadSeq !== loadMoreSeqRef.current) return;
      // Partial paging error（七次复审 P1-2）：第一页的数据仍然有效，失败
      // 的只是「下一页」。绝不动 status/items/hasMore —— 旧实现把它们全
      // 拨成 error，ForwardModal 会把已加载的 50 人瞬间清空只剩错误屏。
      setLoadMoreError(
        e?.response?.data?.error
        || e?.response?.data?.detail
        || '搜索联系人失败',
      );
      setLoadMoreErrorStatus(e?.response?.status ?? null);
    } finally {
      if (seq === requestSeqRef.current && loadSeq === loadMoreSeqRef.current) {
        setLoadingMore(false);
      }
    }
  }, [enabled, type, hasMore, loading, loadingMore, debouncedQuery]);

  const refresh = useCallback(() => {
    // chat：强制重拉全量（会话缓存作废）；user：按当前 query 重拉首页。
    chatCacheRef.current = null;
    return fetchFirstPage();
  }, [fetchFirstPage]);

  // chat 的展示列表 = 缓存全量按当前 query 本地过滤（六次复审 P2-1：
  // 后端已翻全量，本地过滤是在完整数据集上的过滤，不再是旧的
  // 「只拉前 100 再过滤」）。语义与后端 strings.Contains 对齐。
  const items = useMemo<FeishuForwardTarget[]>(() => {
    if (type !== 'chat') return serverItems;
    const q = query.trim();
    if (!q) return serverItems;
    return serverItems.filter((t) => t.name.includes(q));
  }, [type, serverItems, query]);

  return {
    items, loading, loadingMore, error, errorStatus,
    loadMoreError, loadMoreErrorStatus,
    hasMore, status, loadMore, refresh,
  };
}
