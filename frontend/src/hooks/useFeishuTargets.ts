/**
 * useFeishuTargets — 飞书目标的远程搜索（五次复审 P1-3 / P2-7 / P2-8）。
 *
 * Schedule Editor 的投递目标与 FeishuForwardModal 曾各自维护一份目标加载
 * 状态机，且都是「打开时一次性拉取 + 浏览器本地过滤」：
 *   · user 走 /search/v1/user，provider 固定一页 20 条 —— 第 21 个人之后
 *     无论怎么输入姓名都选不到；
 *   · chat 只拉一页 100 条 —— 第 101 个群聊同样不可选（后端已改为按
 *     page_token 翻页到 has_more=false，配合本 hook 的服务端过滤）；
 *   · 搜索词只在已拉到的 N 条里本地过滤，与项目「服务端搜索覆盖全量
 *     数据」的治理原则（Applications/Schedules/Directory 同批收口）冲突；
 *   · Schedule Editor 的失败被 catch(() => []) 静默成「没有目标」，
 *     违反 ERROR ≠ EMPTY；
 *   · ForwardModal 的 loadTargets 没有请求代际 —— 慢的旧查询返回后
 *     会覆盖新查询的结果。
 *
 * 本 hook 的契约：
 *   · user（目录搜索）：空 query 不发请求（UI 提示输入姓名搜索）——
 *     空查询只会拿到「全公司任意前 20 人」；有 query 才服务端搜索，
 *     即使 provider 一页仍为 20，也是「服务器搜索后的前 20 个匹配项」；
 *   · chat：query 由后端按名称过滤，空 query = 全量群聊（后端已翻页）；
 *   · 请求代际（seq）：只有最新 query 的响应可以落地；
 *   · ERROR ≠ EMPTY：失败暴露 error + errorStatus（403/400 = 飞书授权
 *     缺权限），refresh() 重试；
 *   · Surface 关闭（enabled=false）或 sessionKey 变化：立即作废在途
 *     请求并清空会话状态 —— 快速重开的第一帧不闪旧结果/旧错误，
 *     搜索词清空同步落地（不等防抖）。
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { fetchFeishuTargets, type FeishuForwardTarget } from '@/services/shareApi';

export type FeishuTargetType = 'user' | 'chat';

export interface UseFeishuTargetsOptions {
  type: FeishuTargetType;
  /** Surface 是否打开（编辑器打开且开启投递 / 转发弹窗的当前 tab）。 */
  enabled: boolean;
  /** 搜索词（hook 内部防抖后直发后端）。 */
  query: string;
  /** 搜索防抖（默认 300ms）。 */
  debounceMs?: number;
  /**
   * Picker 会话标识（五次复审 §37–§38）：变化 = 新会话，立即作废在途
   * 请求并清空 items/error，且 debouncedQuery 同步为当前 query。
   */
  sessionKey?: string | number | null;
}

export interface UseFeishuTargetsResult {
  items: FeishuForwardTarget[];
  loading: boolean;
  /** 失败信息（ERROR ≠ EMPTY —— 绝不静默成空列表）。 */
  error: string | null;
  /** 失败响应的 HTTP 状态码（403/400 = 需要重新授权飞书）。 */
  errorStatus: number | null;
  /** 按当前 query 重新请求（重试入口）。 */
  refresh: () => Promise<void>;
}

export function useFeishuTargets({
  type, enabled, query, debounceMs = 300, sessionKey,
}: UseFeishuTargetsOptions): UseFeishuTargetsResult {
  const [items, setItems] = useState<FeishuForwardTarget[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [errorStatus, setErrorStatus] = useState<number | null>(null);
  // 请求代际（五次复审 §29）：只有最新 query 的响应可以落地。
  const seqRef = useRef(0);

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

  // 会话切换的 render-time 重置（§37–§38）：sessionKey 变化的那一帧就
  // 清空会话状态并同步 debouncedQuery —— 同一次 commit 里的首屏请求
  // 因此直接带上正确的（已清空的）query，而不是先发一次旧词请求。
  const prevSessionRef = useRef(sessionKey);
  if (prevSessionRef.current !== sessionKey) {
    prevSessionRef.current = sessionKey;
    seqRef.current += 1;
    setItems([]);
    setLoading(false);
    setError(null);
    setErrorStatus(null);
    setDebouncedQuery(query);
  }

  const fetchTargets = useCallback(async () => {
    if (!enabled) return;
    // user 是目录搜索：空 query 不发请求 —— 空查询只会拿到「任意前 20 人」。
    if (type === 'user' && !debouncedQuery.trim()) {
      seqRef.current += 1; // 作废在途搜索（用户把词删空了）
      setItems([]);
      setLoading(false);
      setError(null);
      setErrorStatus(null);
      return;
    }
    const seq = ++seqRef.current;
    setLoading(true);
    setError(null);
    setErrorStatus(null);
    try {
      const list = await fetchFeishuTargets(type, debouncedQuery.trim() || undefined);
      if (seq !== seqRef.current) return; // 旧查询晚到：整体丢弃
      setItems(list);
    } catch (e: any) {
      if (seq !== seqRef.current) return;
      setError(
        e?.response?.data?.error
        || e?.response?.data?.detail
        || (type === 'user' ? '搜索联系人失败' : '获取群聊列表失败'),
      );
      setErrorStatus(e?.response?.status ?? null);
      setItems([]);
    } finally {
      if (seq === seqRef.current) setLoading(false);
    }
  }, [enabled, type, debouncedQuery]);

  useEffect(() => { void fetchTargets(); }, [fetchTargets, sessionKey]);

  // Surface 关闭：立即作废在途请求 + 清空会话状态（重开第一帧不闪旧数据）。
  useEffect(() => {
    if (enabled) return;
    seqRef.current += 1;
    setItems([]);
    setLoading(false);
    setError(null);
    setErrorStatus(null);
  }, [enabled]);

  const refresh = useCallback(() => fetchTargets(), [fetchTargets]);

  return { items, loading, error, errorStatus, refresh };
}
