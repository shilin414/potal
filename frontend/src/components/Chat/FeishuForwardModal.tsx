/**
 * FeishuForwardModal — pick Feishu users/groups and deliver a share snapshot
 * card, mirroring the reference project's 转发到飞书: contact/group tabs,
 * search, multi-select, recent-target history, per-target results.
 *
 * 数据面复用 useFeishuTargets（五次复审 P2-8）：搜索词直发后端（user 走
 * 目录搜索、chat 由后端翻全量后按名称过滤），请求带代际守卫 —— 旧实现
 * loadTargets 没有请求序号，慢的旧查询返回后会覆盖新查询的结果。
 *
 * Scope errors (403) surface a re-authorization action that reuses the
 * existing OAuth start → callback chain, which now requests the forwarding
 * scopes — that is how already-logged-in users top up permissions.
 *
 * Send session guard（八次复审 P1）：发送会话身份 = (open, shareToken)，
 * session epoch + single-flight + stale response guard —— 旧会话在途的发送
 * 响应对新会话 UI 一律无效。旧实现里 close → reopen 后，A 的迟到成功会把
 * 刚打开的 B 弹窗直接关掉，迟到失败会把 B 的选中目标覆盖成 A 的失败目
 * 标，且 B 会继承 A 的 sending 锁死发送按钮。
 *
 * Selection reconcile（九次复审 P1）：异步发送结果只从「当前选中」里移除
 * 本轮已成功的目标 —— snapshot 决定本次发了谁，current 决定用户现在想选
 * 谁；不得用发送开始时的快照整体覆盖 selected（用户在途期间取消的目标会
 * 复活、新选的目标会被删）。
 */
import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Avatar, Empty, Input, Modal, Spin, Tabs, message as antdMessage } from 'antd';
import { CheckOutlined, ReloadOutlined, SendOutlined } from '@ant-design/icons';
import {
  forwardShareToFeishu,
  loadForwardHistory,
  saveForwardHistory,
  type FeishuForwardTarget,
} from '@/services/shareApi';
import { useFeishuTargets } from '@/hooks/useFeishuTargets';
import './FeishuForwardModal.css';

interface FeishuForwardModalProps {
  open: boolean;
  shareToken: string | null;
  onClose: () => void;
}

const HISTORY_LIMIT = 10;

/**
 * 前端 20 目标上限（七次复审 P2-8）：Backend feishuForwardMaxTargets=20、
 * OpenAPI maxItems=20，前端 toggle 必须同限 —— Chat 全量 + user 分页让用户
 * 更容易真实选到 21+，旧实现要点「发送（25）」才被后端 400 拒绝。
 */
const MAX_FORWARD_TARGETS = 20;

const FeishuForwardModal: React.FC<FeishuForwardModalProps> = ({
  open,
  shareToken,
  onClose,
}) => {
  const [tab, setTab] = useState<'user' | 'chat'>('chat');
  const [query, setQuery] = useState('');
  const [needReauth, setNeedReauth] = useState(false);
  const [selected, setSelected] = useState<FeishuForwardTarget[]>([]);
  const [history, setHistory] = useState<FeishuForwardTarget[]>([]);
  const [sending, setSending] = useState(false);

  // 发送会话代际（八次复审 P1）：会话身份 = (open, shareToken)，任一变化
  // 即新会话 —— close → reopen 同一分享、A 分享 → B 分享都算换会话。与
  // Schedule Editor 的 editorEpochRef 同一套已验证模型：session epoch +
  // single-flight + stale response guard。
  const sessionEpochRef = useRef(0);
  // single-flight：同会话内已有发送在途时，后续触发直接拒绝（双击发送只
  // 发一次 API 请求）。
  const sendInFlightRef = useRef<{ epoch: number } | null>(null);

  // 会话失效是 session identity invalidation，须在 commit 后同步完成
  // （useLayoutEffect 而非 useEffect）：新会话打开的那一帧 sending 已复
  // 位、旧 in-flight 已作废，不留给旧响应操纵新会话 UI 的窗口。
  useLayoutEffect(() => {
    sessionEpochRef.current += 1;
    sendInFlightRef.current = null;
    setSending(false);
  }, [open, shareToken]);

  useEffect(() => {
    if (!open) return;
    setHistory(loadForwardHistory());
  }, [open]);

  // 两个 tab 的目标都走共享远程搜索 hook（五次复审 P2-8）：请求代际保证
  // 只有最新 query 的响应落地。非活跃 tab 的 hook 完全静默（enabled=false）
  // —— 后端已按 page_token 翻全量页，切走 tab 时在后台整轮拉群聊既浪费
  // 飞书 API 配额也毫无可见性；切回时按当前 query 重拉一遍即可。
  // sessionKey：每次打开都是新会话，关闭即清空，重开不闪旧结果。
  const chats = useFeishuTargets({
    type: 'chat',
    enabled: open && tab === 'chat',
    query: query.trim(),
    sessionKey: open ? 'feishu-forward' : null,
  });
  const users = useFeishuTargets({
    type: 'user',
    enabled: open && tab === 'user',
    query: query.trim(),
    sessionKey: open ? 'feishu-forward' : null,
  });

  const active = tab === 'chat' ? chats : users;
  const targets = active.items;
  const loading = active.loading;
  // idle = 未参与查询（user 空 query 不发请求，六次复审 P2-3）≠ 0 条结果。
  const idleSearch = tab === 'user' && active.status === 'idle';
  // 目标接口 403/400 = 飞书授权缺权限（派生自最新失败，不另存状态）。
  // 续拉 403 同样是授权变化（七次复审 §23）：分页错误拆分后不能丢掉
  // 重新授权入口。
  const authError = active.errorStatus === 403
    || active.errorStatus === 400
    || active.loadMoreErrorStatus === 403;
  const showReauth = needReauth || authError;

  const reset = () => {
    setTab('chat');
    setQuery('');
    setSelected([]);
    setNeedReauth(false);
  };

  const close = () => {
    reset();
    onClose();
  };

  // toggle 是唯一的选中入口（列表行 + 最近转发 chip 都走它，七次复审
  // §35），上限写在 toggle 里两个入口同时生效。
  const toggle = (t: FeishuForwardTarget) => {
    if (selected.some((x) => x.id === t.id)) {
      setSelected(selected.filter((x) => x.id !== t.id));
      return;
    }
    if (selected.length >= MAX_FORWARD_TARGETS) {
      antdMessage.warning('一次最多转发给 20 个目标');
      return;
    }
    setSelected([...selected, t]);
  };

  const handleSend = async () => {
    const epoch = sessionEpochRef.current;
    if (!shareToken || !selected.length
      || sendInFlightRef.current?.epoch === epoch) {
      return;
    }
    sendInFlightRef.current = { epoch };

    // snapshot（八次复审 §21）：API 请求、历史、失败目标 reconcile 全部
    // 基于同一批发送目标，不混用发送结束时可能已经变化的 selected /
    // shareToken（闭包里虽是旧值，显式命名让这个不变式可见）。
    const selectedSnapshot = selected;
    const shareTokenSnapshot = shareToken;

    setSending(true);
    try {
      const result = await forwardShareToFeishu(
        shareTokenSnapshot,
        selectedSnapshot.map((t) => ({ target_type: t.target_type, id: t.id })),
      );
      // 旧会话的迟到响应对新会话 UI 一律无效，直接丢弃（八次复审 §22）：
      // 不能 message / setSelected / setNeedReauth / close / setSending ——
      // 这些副作用已经不属于当前 session。后端转发本身无法撤销，守卫的
      // 边界是「旧响应不操纵新会话的 UI」。
      if (sessionEpochRef.current !== epoch) return;

      const succeededIds = new Set(
        result.results.filter((r) => r.ok).map((r) => r.target_id),
      );
      if (succeededIds.size) {
        saveForwardHistory(
          selectedSnapshot.filter((t) => succeededIds.has(t.id)),
        );
      }
      if (result.fail_count === 0) {
        antdMessage.success(`已发送给 ${result.success_count} 个目标`);
        close();
      } else {
        const firstError = result.results.find((r) => !r.ok)?.error;
        antdMessage.error(
          `${result.success_count} 个成功，${result.fail_count} 个失败${firstError ? `：${firstError}` : ''}`,
        );
        // 重新授权看「是否存在授权失败」而不是「是否全部失败」（八次复审
        // P2）：1 成功 + 1 授权失败的混合结果同样要给重新授权入口 —— 旧
        // 条件 success_count === 0 让用户只能对着注定失败的目标反复重试。
        const authFailure = result.results.find(
          (r) => !r.ok && r.error?.includes('重新授权'),
        );
        if (authFailure) {
          setNeedReauth(true);
        }
        // Keep the modal open so the sender can retry failed targets
        // (七次复审 P2-9)：成功的自动移除、失败的保持选中 —— 旧的
        // setSelected([]) 把失败目标也取消选择，用户得从头再挑一遍。
        // reconcile 只做「从当前选中里移除本轮已成功的目标」（九次复审
        // P1）：snapshot 决定本次发了谁，current 决定用户现在想选谁 ——
        // 发送在途期间列表 / Tab / 搜索 / chip 都可操作，旧实现
        // setSelected(snapshot 失败目标) 把发送开始时的快照当成发送结束时
        // 整个 UI 的真相：用户刚取消的失败目标复活、新选的目标被删、切
        // Tab 后旧目标整组写回。
        setSelected((current) => current.filter((t) => !succeededIds.has(t.id)));
      }
    } catch (e: any) {
      if (sessionEpochRef.current !== epoch) return;
      antdMessage.error(e?.response?.data?.detail || '转发失败，请稍后重试');
    } finally {
      // 会话已切换时 sending 已由 useLayoutEffect 复位、in-flight 已被清
      // 空，都不归这个旧 closure 管。
      if (sessionEpochRef.current === epoch) {
        setSending(false);
      }
      if (sendInFlightRef.current?.epoch === epoch) {
        sendInFlightRef.current = null;
      }
    }
  };

  const handleReauth = () => {
    const returnTo = window.location.pathname + window.location.search;
    window.location.href = `/api/identity/oauth/start?return_to=${encodeURIComponent(returnTo)}`;
  };

  const recent = useMemo(
    () => history.slice(0, HISTORY_LIMIT),
    [history],
  );

  return (
    <Modal
      open={open}
      onCancel={close}
      footer={null}
      title="转发到飞书"
      width={460}
      centered
      destroyOnClose
    >
      {showReauth ? (
        <div className="ffm-reauth">
          <p>当前飞书授权缺少转发所需权限（IM / 通讯录）。</p>
          <p className="ffm-reauth__hint">
            点击下方按钮重新授权后，回到本页再次发起转发即可。
          </p>
          <button className="ffm-reauth__btn" onClick={handleReauth}>
            <ReloadOutlined /> 重新授权飞书
          </button>
        </div>
      ) : (
        <div className="ffm-body">
          <Tabs
            activeKey={tab}
            onChange={(k) => { setTab(k as 'user' | 'chat'); setSelected([]); }}
            items={[
              { key: 'chat', label: '群聊' },
              { key: 'user', label: '联系人' },
            ]}
            size="small"
          />
          <Input
            placeholder={tab === 'user' ? '输入姓名搜索联系人' : '搜索群聊名称'}
            value={query}
            allowClear
            onChange={(e) => setQuery(e.target.value)}
          />

          {recent.length > 0 && (
            <div className="ffm-recent">
              <span className="ffm-recent__label">最近转发</span>
              <div className="ffm-recent__chips">
                {recent.map((t) => (
                  <button
                    key={`${t.target_type}-${t.id}`}
                    className={`ffm-recent-chip ${selected.some((x) => x.id === t.id) ? 'ffm-recent-chip--on' : ''}`}
                    title={t.name}
                    onClick={() => toggle(t)}
                  >
                    {t.name}
                  </button>
                ))}
              </div>
            </div>
          )}

          <div className="ffm-list">
            {/* 首页加载失败 ≠ 没有目标（五次复审 §28，ERROR ≠ EMPTY）：错误
                可见 + 可重试，不再静默空列表。分页失败（loadMoreError）不进
                这个分支 —— 已加载的行保持可见，只在底部给重试（七次复审
                P1-2）。 */}
            {active.error ? (
              <div className="ffm-list__center ffm-list__error">
                <Empty
                  image={Empty.PRESENTED_IMAGE_SIMPLE}
                  description={active.error}
                />
                <button type="button" className="ffm-error-retry" onClick={() => void active.refresh()}>
                  <ReloadOutlined /> 重试
                </button>
              </div>
            ) : loading ? (
              <div className="ffm-list__center"><Spin /></div>
            ) : targets.length === 0 ? (
              <div className="ffm-list__center">
                <Empty
                  image={Empty.PRESENTED_IMAGE_SIMPLE}
                  description={idleSearch
                    ? '输入姓名搜索联系人'
                    : '没有匹配的结果'}
                />
              </div>
            ) : (
              <>
                {targets.map((t) => {
                  const on = selected.some((x) => x.id === t.id);
                  return (
                    <button
                      key={t.id}
                      className={`ffm-row ${on ? 'ffm-row--on' : ''}`}
                      onClick={() => toggle(t)}
                    >
                      <Avatar size={32} src={t.avatar_url || undefined}>
                        {(t.name || '?').slice(0, 1)}
                      </Avatar>
                      <span className="ffm-row__name" title={t.name}>{t.name}</span>
                      <span className="ffm-row__type">
                        {t.target_type === 'chat' ? '群聊' : '联系人'}
                      </span>
                      <span className={`ffm-row__check ${on ? 'ffm-row__check--on' : ''}`}>
                        {on && <CheckOutlined />}
                      </span>
                    </button>
                  );
                })}
                {/* 联系人 cursor 分页（六次复审 P1-3）：匹配的第 51+ 人靠
                    续拉补齐 —— 旧的「一页 20 条」让第 21 人永远选不到；
                    chat 恒全量，不显示此入口。分页失败时与「重试加载」互
                    斥（八次复审 P2）：两个按钮调的都是 loadMore，同时出
                    现只是两个并排的重复动作入口。 */}
                {active.hasMore && !active.loadMoreError && (
                  <button
                    type="button"
                    className="ffm-load-more"
                    disabled={active.loadingMore}
                    onClick={() => void active.loadMore()}
                  >
                    {active.loadingMore ? '加载中…' : '加载更多'}
                  </button>
                )}
                {/* 分页失败 ≠ 整个数据源失败（七次复审 P1-2）：已加载的人
                    仍然有效，只提示「更多加载失败」+ 重试失败的那一页 ——
                    重试调 loadMore（从断点续拉），不是 refresh（回第一页
                    丢掉全部进度）。 */}
                {active.loadMoreError && (
                  <div className="ffm-paging-error">
                    <span className="ffm-paging-error__text">
                      {active.loadMoreError || '更多联系人加载失败'}
                    </span>
                    <button
                      type="button"
                      className="ffm-paging-error__retry"
                      disabled={active.loadingMore}
                      onClick={() => void active.loadMore()}
                    >
                      <ReloadOutlined /> 重试加载
                    </button>
                  </div>
                )}
              </>
            )}
          </div>

          <div className="ffm-footer">
            <button className="ffm-cancel" onClick={close}>取消</button>
            <button
              className="ffm-send"
              disabled={!selected.length || sending}
              onClick={() => void handleSend()}
            >
              <SendOutlined /> {sending ? '发送中…' : `发送（${selected.length}）`}
            </button>
          </div>
        </div>
      )}
    </Modal>
  );
};

export default FeishuForwardModal;
