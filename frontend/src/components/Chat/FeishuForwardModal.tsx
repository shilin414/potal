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
 */
import React, { useEffect, useMemo, useState } from 'react';
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
  const authError = active.errorStatus === 403 || active.errorStatus === 400;
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

  const toggle = (t: FeishuForwardTarget) => {
    setSelected((current) => (
      current.some((x) => x.id === t.id)
        ? current.filter((x) => x.id !== t.id)
        : [...current, t]
    ));
  };

  const handleSend = async () => {
    if (!shareToken || !selected.length) return;
    setSending(true);
    try {
      const result = await forwardShareToFeishu(
        shareToken,
        selected.map((t) => ({ target_type: t.target_type, id: t.id })),
      );
      const succeeded = result.results.filter((r) => r.ok);
      if (succeeded.length) {
        saveForwardHistory(
          selected.filter((t) => succeeded.some((r) => r.target_id === t.id)),
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
        if (result.fail_count > 0 && result.success_count === 0
          && firstError?.includes('重新授权')) {
          setNeedReauth(true);
        }
        // Keep the modal open so the sender can retry failed targets.
        setSelected([]);
      }
    } catch (e: any) {
      antdMessage.error(e?.response?.data?.detail || '转发失败，请稍后重试');
    } finally {
      setSending(false);
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
            {/* 加载失败 ≠ 没有目标（五次复审 §28，ERROR ≠ EMPTY）：错误
                可见 + 可重试，不再静默空列表。 */}
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
                    chat 恒全量，不显示此入口。 */}
                {active.hasMore && (
                  <button
                    type="button"
                    className="ffm-load-more"
                    disabled={active.loadingMore}
                    onClick={() => void active.loadMore()}
                  >
                    {active.loadingMore ? '加载中…' : '加载更多'}
                  </button>
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
