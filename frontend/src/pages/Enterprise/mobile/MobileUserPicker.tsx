/**
 * MobileUserPicker — 移动端人员选择 Bottom Sheet（开发执行报告 §43）。
 *
 * 多选 + 搜索（enterpriseApi.users({q})，300ms 防抖由调用侧 search 触发）。
 * 底部固定「已选择 N 人 · 完成」。与桌面 Select mode="multiple" 同一数据
 * 源，仅交互换成了适合手指的行选择。
 */
import React, { useEffect, useState } from 'react';
import { Avatar, Drawer, Input } from 'antd';
import { CheckOutlined, SearchOutlined } from '@ant-design/icons';
import { enterpriseApi, type DirectoryUser } from '../enterpriseApi';
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

const USER_PAGE_LIMIT = 100;

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
  const [results, setResults] = useState<DirectoryUser[]>([]);
  const [loading, setLoading] = useState(false);
  const [picked, setPicked] = useState<MobilePickedUser[]>([]);

  useEffect(() => {
    if (open) {
      setPicked(selectedUsers.map((u) => ({ ...u })));
      setQuery('');
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  useEffect(() => {
    if (!open) return undefined;
    let stale = false;
    setLoading(true);
    const timer = setTimeout(() => {
      enterpriseApi.users({ q: query.trim() || undefined, limit: USER_PAGE_LIMIT })
        .then((page) => { if (!stale) setResults(page.results); })
        .catch(() => { if (!stale) setResults([]); })
        .finally(() => { if (!stale) setLoading(false); });
    }, 300);
    return () => {
      stale = true;
      clearTimeout(timer);
    };
  }, [open, query]);

  const pickedIds = new Set(picked.map((u) => u.id));

  const toggle = (user: DirectoryUser) => {
    setPicked((current) => {
      if (current.some((u) => u.id === user.id)) {
        return current.filter((u) => u.id !== user.id);
      }
      return [...current, toPicked(user)];
    });
  };

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
              placeholder="搜索姓名 / 工号"
              prefix={<SearchOutlined style={{ color: 'var(--color-text-dim)' }} />}
              onChange={(e) => setQuery(e.target.value)}
            />
          </div>
          <div className="mobile-picker__body">
            {loading && results.length === 0 ? (
              <div className="mobile-console-empty">搜索中…</div>
            ) : results.length === 0 ? (
              <div className="mobile-console-empty">没有匹配的人员</div>
            ) : results.map((user) => {
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
