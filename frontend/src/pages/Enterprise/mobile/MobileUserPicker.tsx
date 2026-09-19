/**
 * MobileUserPicker — 移动端人员选择 Bottom Sheet（开发执行报告 §43）。
 *
 * 多选 + 搜索。数据面复用 useDirectoryUsers（二次复审 P2-5）：
 *   · 服务端搜索（防抖 300ms，q 直发后端）；
 *   · cursor 分页 + 加载更多 —— 不再被 100 人上限截断；
 *   · ACL 选人只看有效员工（include_inactive: false，后端默认值）；
 *   · 请求失败是错误态 + 重试，绝不伪装成“没有匹配的人员”；
 *   · 跨页选择保留（picked 独立于当前结果页）。
 * 底部固定「已选择 N 人 · 完成」。与桌面 Select mode="multiple" 同一数据源。
 */
import React, { useEffect, useState } from 'react';
import { Avatar, Button, Drawer, Input } from 'antd';
import { CheckOutlined, SearchOutlined } from '@ant-design/icons';
import type { DirectoryUser } from '../enterpriseApi';
import { useDirectoryUsers } from '../hooks/useDirectoryUsers';
import '../EnterpriseMobile.css';

export interface MobilePickedUser {
  id: number;
  name: string;
  avatar_url: string;
  departments: string[];
}

export interface MobileUserPickerProps {
  open: boolean;
  /** 已授权人员（编辑器当前 policy.users，按 id 归一）。 */
  selectedUsers: MobilePickedUser[];
  onClose: () => void;
  onDone: (users: MobilePickedUser[]) => void;
}

/** A search result (DirectoryUser) flattened to the picker's selection shape. */
const toPicked = (user: DirectoryUser): MobilePickedUser => ({
  id: user.id,
  name: user.name,
  avatar_url: user.avatar_url,
  departments: (user.departments || []).map((d) => d.name),
});

export default function MobileUserPicker({
  open, selectedUsers, onClose, onDone,
}: MobileUserPickerProps) {
  const [query, setQuery] = useState('');
  const [picked, setPicked] = useState<MobilePickedUser[]>([]);

  // 关闭时立即清空 query（三次复审 §46）：否则重开的第一帧仍会带着上一次
  // 的搜索词先发一次请求，再被清空重发一次。开启时重置已选与搜索词。
  useEffect(() => {
    if (!open) {
      setQuery('');
      return;
    }
    setPicked(selectedUsers.map((u) => ({ ...u })));
    setQuery('');
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const {
    items: results,
    loading,
    loadingMore,
    hasMore,
    error,
    loadMore,
    refresh,
  } = useDirectoryUsers({
    query,
    enabled: open,
    includeInactive: false,
    // Picker 会话（五次复审 §37–§38）：关闭 → null、打开 → 'picker'。
    // 每次开关周期都是新会话：立即作废在途请求并清空旧会话的
    // items/error，配合上面的 query 清空 + hook 的同步防抖，快速关闭
    // 重开的第一帧不再闪旧词请求或旧结果。
    sessionKey: open ? 'mobile-user-picker' : null,
  });

  const pickedIds = new Set(picked.map((u) => u.id));

  const toggle = (user: DirectoryUser) => {
    setPicked((current) => {
      if (current.some((u) => u.id === user.id)) {
        return current.filter((u) => u.id !== user.id);
      }
      return [...current, toPicked(user)];
    });
  };

  // A first-page failure is a full error state; a failed loadMore keeps the
  // rows and swaps the 加载更多 CTA for a retry (ERROR != EMPTY, P2-5).
  const fatalError = Boolean(error) && results.length === 0;
  // A failed loadMore keeps its cursor (retry = loadMore); a failed search /
  // first page clears it (retry = refresh) — pick by what the hook still offers.
  const retryPartial = () => (hasMore ? void loadMore() : void refresh());

  return (
    <Drawer
      placement="bottom"
      open={open}
      onClose={onClose}
      height="72dvh"
      closable={false}
      title={null}
      rootClassName="mobile-bottom-sheet"
      styles={{
        content: { borderRadius: '24px 24px 0 0' },
        body: { padding: 0, display: 'flex', flexDirection: 'column', minHeight: 0 },
      }}
    >
      <div className="mobile-sheet">
        <div className="mobile-sheet__handle" aria-hidden />
        <div className="mobile-sheet__header">
          <span className="mobile-sheet__title">选择人员</span>
        </div>
        <div className="mobile-picker">
          <div className="mobile-picker__search">
            <Input
              allowClear
              value={query}
              placeholder="搜索姓名"
              prefix={<SearchOutlined style={{ color: 'var(--color-text-dim)' }} />}
              onChange={(e) => setQuery(e.target.value)}
            />
          </div>
          <div className="mobile-picker__body">
            {fatalError ? (
              <div className="mobile-console-empty">
                <strong>加载人员失败</strong>
                <Button onClick={() => void refresh()}>重试</Button>
              </div>
            ) : loading && results.length === 0 ? (
              <div className="mobile-console-empty">搜索中…</div>
            ) : results.length === 0 ? (
              <div className="mobile-console-empty">没有匹配的人员</div>
            ) : (
              <>
                {results.map((user) => {
                  const checked = pickedIds.has(user.id);
                  return (
                    <button
                      key={user.id}
                      type="button"
                      className="mobile-picker__row"
                      aria-pressed={checked}
                      onClick={() => toggle(user)}
                    >
                      <Avatar src={user.avatar_url} size={36}>
                        {user.name.slice(0, 1)}
                      </Avatar>
                      <span className="mobile-picker__row-body">
                        <span className="mobile-picker__row-name">{user.name}</span>
                        <span className="mobile-picker__row-meta">
                          {user.departments.map((d) => d.name).join(' / ') || '—'}
                        </span>
                      </span>
                      <span className={`mobile-picker__check${checked ? ' mobile-picker__check--on' : ''}`}>
                        <CheckOutlined />
                      </span>
                    </button>
                  );
                })}
                {/* One CTA, never two: a failed loadMore REPLACES 加载更多. */}
                {error ? (
                  <button
                    type="button"
                    className="mobile-console-more"
                    onClick={retryPartial}
                  >
                    加载失败，点击重试
                  </button>
                ) : hasMore ? (
                  <button
                    type="button"
                    className="mobile-console-more"
                    onClick={() => void loadMore()}
                  >
                    {loadingMore ? '加载中…' : '加载更多'}
                  </button>
                ) : null}
              </>
            )}
          </div>
          <div className="mobile-picker__footer">
            <span className="mobile-picker__footer-count">已选择 {picked.length} 人</span>
            <button
              type="button"
              className="mobile-picker__done"
              onClick={() => onDone(picked)}
            >
              完成
            </button>
          </div>
        </div>
      </div>
    </Drawer>
  );
}
