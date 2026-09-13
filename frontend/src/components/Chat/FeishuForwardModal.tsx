/**
 * FeishuForwardModal — pick Feishu users/groups and deliver a share snapshot
 * card, mirroring the reference project's 转发到飞书: contact/group tabs,
 * search, multi-select, recent-target history, per-target results.
 *
 * Scope errors (403) surface a re-authorization action that reuses the
 * existing OAuth start → callback chain, which now requests the forwarding
 * scopes — that is how already-logged-in users top up permissions.
 */
import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Avatar, Empty, Input, Modal, Spin, Tabs, message as antdMessage } from 'antd';
import { CheckOutlined, ReloadOutlined, SendOutlined } from '@ant-design/icons';
import {
  fetchFeishuTargets,
  forwardShareToFeishu,
  loadForwardHistory,
  saveForwardHistory,
  type FeishuForwardTarget,
} from '@/services/shareApi';
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
  const [targets, setTargets] = useState<FeishuForwardTarget[]>([]);
  const [loading, setLoading] = useState(false);
  const [needReauth, setNeedReauth] = useState(false);
  const [selected, setSelected] = useState<FeishuForwardTarget[]>([]);
  const [history, setHistory] = useState<FeishuForwardTarget[]>([]);
  const [sending, setSending] = useState(false);
  const searchTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  /** Chat list is fetched once per open and filtered locally. */
  const chatCache = useRef<FeishuForwardTarget[]>([]);

  useEffect(() => {
    if (!open) return;
    setHistory(loadForwardHistory());
  }, [open]);

  const loadTargets = async (type: 'user' | 'chat', q: string) => {
    setLoading(true);
    setNeedReauth(false);
    try {
      const list = await fetchFeishuTargets(type, q || undefined);
      if (type === 'chat') chatCache.current = list;
      setTargets(list);
    } catch (e: any) {
      const status = e?.response?.status;
      if (status === 403 || status === 400) {
        setNeedReauth(true);
      } else {
        antdMessage.error(e?.response?.data?.error || '获取转发目标失败');
      }
      setTargets([]);
    } finally {
      setLoading(false);
    }
  };

  // (Re)load when the modal opens or the tab changes. Chat loads once and
  // filters locally; user search is debounced server-side.
  useEffect(() => {
    if (!open || needReauth) return;
    if (tab === 'chat') {
      if (chatCache.current.length === 0) void loadTargets('chat', '');
      else setTargets(
        chatCache.current.filter((t) => t.name.includes(query.trim())),
      );
    } else {
      if (searchTimer.current) clearTimeout(searchTimer.current);
      if (!query.trim()) {
        setTargets([]);
        setLoading(false);
        return;
      }
      searchTimer.current = setTimeout(() => {
        void loadTargets('user', query.trim());
      }, 400);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, tab, query]);

  const reset = () => {
    setTab('chat');
    setQuery('');
    setTargets([]);
    setSelected([]);
    setNeedReauth(false);
    chatCache.current = [];
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
      {needReauth ? (
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
            {loading ? (
              <div className="ffm-list__center"><Spin /></div>
            ) : targets.length === 0 ? (
              <div className="ffm-list__center">
                <Empty
                  image={Empty.PRESENTED_IMAGE_SIMPLE}
                  description={tab === 'user' && !query.trim()
                    ? '输入姓名搜索联系人'
                    : '没有匹配的结果'}
                />
              </div>
            ) : (
              targets.map((t) => {
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
              })
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
