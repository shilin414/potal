/**
 * MobileDirectoryPage — 移动端部门与人员（开发执行报告 §44–§46，二次复审 P1-4）。
 *
 * Segmented 切换 部门/人员，各自带独立搜索词；只读快照（飞书 Directory），
 * 部门详情第一版不做。
 *
 * 二次复审 P1-4 修复：
 *   · 人员走 useDirectoryUsers（cursor 分页 + 服务端搜索）——员工 >100
 *     可继续「加载更多」；
 *   · 部门/人员搜索词彻底拆分：部门是全量快照只做本地过滤（搜索部门不再
 *     顺带请求人员 API），切 Tab 不再继承上一个 Tab 的搜索词；
 *   · 人数标题不再把「当前已加载数」显示成企业总人数（无 total 时只显示
 *     「人员」）；
 *   · 请求失败 ≠ 没有匹配：部门/人员各有独立错误态与重试。
 */
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Avatar, Button, Segmented, Skeleton, Tag } from 'antd';
import {
  enterpriseApi,
  type DirectoryDepartment,
} from '../enterpriseApi';
import { useDirectoryUsers } from '../hooks/useDirectoryUsers';
import {
  MobileEmptyState,
  MobilePage,
  MobileSearchBar,
} from '@/components/MobileConsole';
import '../EnterpriseMobile.css';

type DirectoryTab = 'departments' | 'users';

export default function MobileDirectoryPage() {
  const [tab, setTab] = useState<DirectoryTab>('departments');
  // 搜索词按 Tab 拆分（P1-4）：互不继承、互不触发对方的请求。
  const [departmentQuery, setDepartmentQuery] = useState('');
  const [userQuery, setUserQuery] = useState('');

  const [deps, setDeps] = useState<DirectoryDepartment[]>([]);
  const [depsLoading, setDepsLoading] = useState(true);
  const [depsError, setDepsError] = useState<string | null>(null);

  // 部门是全量快照：进入加载一次，之后只在本地过滤。
  const loadDeps = useCallback(async () => {
    setDepsLoading(true);
    setDepsError(null);
    try {
      setDeps(await enterpriseApi.departments({ include_inactive: true }));
    } catch {
      setDepsError('加载部门失败');
    } finally {
      setDepsLoading(false);
    }
  }, []);
  useEffect(() => { void loadDeps(); }, [loadDeps]);

  // 人员：服务端搜索（debounce 在 hook 里）+ cursor 分页；只在 users Tab 激活。
  const {
    items: users,
    loading: usersLoading,
    loadingMore,
    hasMore,
    error: usersError,
    loadMore,
    refresh: refreshUsers,
  } = useDirectoryUsers({ query: userQuery, enabled: tab === 'users' });

  const usersFatalError = Boolean(usersError) && users.length === 0;
  const usersPartialError = Boolean(usersError) && users.length > 0;
  // A failed loadMore keeps its cursor (retry = loadMore); a failed search /
  // first page clears it (retry = refresh).
  const retryUsers = () => (hasMore ? void loadMore() : void refreshUsers());

  const filteredDeps = useMemo(() => {
    const query = departmentQuery.trim().toLowerCase();
    if (!query) return deps;
    return deps.filter((d) => d.name.toLowerCase().includes(query));
  }, [deps, departmentQuery]);

  const query = tab === 'departments' ? departmentQuery : userQuery;
  const setQuery = tab === 'departments' ? setDepartmentQuery : setUserQuery;

  const userRows = (
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
  );

  return (
    <MobilePage>
      <div className="mobile-console-page__sticky">
        <Segmented
          block
          value={tab}
          onChange={(v) => setTab(v as DirectoryTab)}
          options={[
            // 部门是全量快照，计数准确；人员是分页数据，已加载数不是企业
            // 总人数（P1-4），不显示数字。
            { value: 'departments', label: `部门 ${deps.length}` },
            { value: 'users', label: '人员' },
          ]}
        />
        <MobileSearchBar
          placeholder={tab === 'departments' ? '搜索部门' : '搜索姓名'}
          value={query}
          onChange={setQuery}
        />
      </div>

      {tab === 'departments' ? (
        depsError ? (
          <MobileEmptyState
            title="加载部门失败"
            action={<Button onClick={() => void loadDeps()}>重试</Button>}
          />
        ) : depsLoading && deps.length === 0 ? (
          <div style={{ padding: '12px 0' }}>
            <Skeleton active avatar paragraph={{ rows: 1 }} />
            <Skeleton active avatar paragraph={{ rows: 1 }} />
          </div>
        ) : filteredDeps.length === 0 ? (
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
        usersFatalError ? (
          <MobileEmptyState
            title="加载人员失败"
            action={<Button onClick={() => void refreshUsers()}>重试</Button>}
          />
        ) : usersLoading && users.length === 0 ? (
          <div style={{ padding: '12px 0' }}>
            <Skeleton active avatar paragraph={{ rows: 1 }} />
            <Skeleton active avatar paragraph={{ rows: 1 }} />
          </div>
        ) : users.length === 0 ? (
          <MobileEmptyState title="没有匹配的人员" />
        ) : (
          <>
            {userRows}
            {(hasMore || usersPartialError) && (
              <button
                type="button"
                className="mobile-console-more"
                onClick={retryUsers}
              >
                {loadingMore ? '加载中…' : usersPartialError ? '加载失败，点击重试' : '加载更多'}
              </button>
            )}
          </>
        )
      )}
    </MobilePage>
  );
}
