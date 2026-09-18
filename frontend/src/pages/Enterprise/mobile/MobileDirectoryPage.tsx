/**
 * MobileDirectoryPage — 移动端部门与人员（开发执行报告 §44–§46）。
 *
 * Segmented 切换 部门/人员，各自带搜索；只读快照（飞书 Directory），
 * 部门详情第一版不做。
 */
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Avatar, Segmented, Skeleton, Tag } from 'antd';
import {
  enterpriseApi,
  type DirectoryDepartment,
  type DirectoryUser,
} from '../enterpriseApi';
import {
  MobileEmptyState,
  MobilePage,
  MobileSearchBar,
} from '@/components/MobileConsole';
import '../EnterpriseMobile.css';

type DirectoryTab = 'departments' | 'users';

export default function MobileDirectoryPage() {
  const [tab, setTab] = useState<DirectoryTab>('departments');
  const [deps, setDeps] = useState<DirectoryDepartment[]>([]);
  const [users, setUsers] = useState<DirectoryUser[]>([]);
  const [loading, setLoading] = useState(true);
  const [q, setQ] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [d, u] = await Promise.all([
        enterpriseApi.departments({ include_inactive: true }),
        enterpriseApi.users({ q, include_inactive: true, limit: 100 }),
      ]);
      setDeps(d);
      setUsers(u.results);
    } finally {
      setLoading(false);
    }
  }, [q]);

  useEffect(() => {
    void load();
  }, [load]);

  const filteredDeps = useMemo(() => {
    const query = q.trim().toLowerCase();
    if (!query) return deps;
    return deps.filter((d) => d.name.toLowerCase().includes(query));
  }, [deps, q]);

  return (
    <MobilePage>
      <div className="mobile-console-page__sticky">
        <Segmented
          block
          value={tab}
          onChange={(v) => setTab(v as DirectoryTab)}
          options={[
            { value: 'departments', label: `部门 ${deps.length}` },
            { value: 'users', label: `人员 ${users.length}` },
          ]}
        />
        <MobileSearchBar
          placeholder={tab === 'departments' ? '搜索部门' : '搜索人员'}
          value={q}
          onChange={setQ}
        />
      </div>

      {loading && deps.length === 0 && users.length === 0 ? (
        <div style={{ padding: '12px 0' }}>
          <Skeleton active avatar paragraph={{ rows: 1 }} />
          <Skeleton active avatar paragraph={{ rows: 1 }} />
        </div>
      ) : tab === 'departments' ? (
        filteredDeps.length === 0 ? (
          <MobileEmptyState title="没有匹配的部门" />
        ) : (
          <div className="mobile-console-section__rows" style={{ marginTop: 12 }}>
            {filteredDeps.map((dep) => (
              <div key={dep.id} className="mobile-console-row" style={{ cursor: 'default' }}>
                <span className="mobile-console-row__body">
                  <span className="mobile-console-row__title">
                    <span>{dep.name}</span>
                  </span>
                </span>
                <Tag color={dep.is_active ? 'green' : 'default'}>
                  {dep.is_active ? '有效' : '停用'}
                </Tag>
              </div>
            ))}
          </div>
        )
      ) : (
        users.length === 0 ? (
          <MobileEmptyState title="没有匹配的人员" />
        ) : (
          <div className="mobile-console-section__rows" style={{ marginTop: 12 }}>
            {users.map((user) => (
              <div key={user.id} className="mobile-console-row" style={{ cursor: 'default' }}>
                <Avatar src={user.avatar_url} size={44}>
                  {user.name.slice(0, 1)}
                </Avatar>
                <span className="mobile-console-row__body">
                  <span className="mobile-console-row__title">
                    <span>{user.name}</span>
                  </span>
                  <span className="mobile-console-row__meta">
                    {user.departments.map((d) => d.name).join(' / ') || '—'}
                  </span>
                </span>
                <span style={{ display: 'flex', flex: 'none', flexDirection: 'column', gap: 4, alignItems: 'flex-end' }}>
                  <Tag color={user.is_active ? 'green' : 'red'}>
                    {user.is_active ? '在职' : '离职'}
                  </Tag>
                  {user.local_user_id ? (
                    <Tag color="blue">Potal 已关联</Tag>
                  ) : (
                    <Tag>未登录</Tag>
                  )}
                </span>
              </div>
            ))}
          </div>
        )
      )}
    </MobilePage>
  );
}
