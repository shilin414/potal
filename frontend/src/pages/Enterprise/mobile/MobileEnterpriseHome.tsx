/**
 * MobileEnterpriseHome — 企业控制台移动首页（开发执行报告 §32–§34）：
 * 概览 StatCard（2 列）+ 资源/权限/组织/平台四组 SettingsGroup 菜单。
 * 导航 key 与桌面 Sider 完全一致（§88）。
 */
import React, { useEffect, useState } from 'react';
import { Alert } from 'antd';
import { enterpriseApi, type DirectoryStats, type SyncRun } from '../enterpriseApi';
import { ENTERPRISE_SECTIONS, pagePath } from '../enterpriseNav';
import { useNavigate } from 'react-router-dom';
import {
  MobilePage,
  MobileSection,
  MobileSettingsGroup,
  MobileSettingsRow,
  MobileStatCard,
} from '@/components/MobileConsole';
import '../EnterpriseMobile.css';

const STATUS_LABEL: Record<string, string> = {
  success: '成功', failed: '失败', running: '进行中', pending: '等待中',
};

export default function MobileEnterpriseHome() {
  const navigate = useNavigate();
  const [stats, setStats] = useState<DirectoryStats | null>(null);
  const [runs, setRuns] = useState<SyncRun[]>([]);

  useEffect(() => {
    void Promise.all([enterpriseApi.stats(), enterpriseApi.syncRuns(1)])
      .then(([s, r]) => {
        setStats(s);
        setRuns(r);
      })
      .catch(() => {/* 首页保持空态，不阻塞菜单 */});
  }, []);

  const oauthMatch = stats && stats.oauth_users
    ? Math.round((stats.linked_directory_users * 100) / stats.oauth_users)
    : 0;

  return (
    <MobilePage>
      <MobileSection title="企业概览">
        <div className="mobile-console-stat-grid">
          <MobileStatCard
            value={stats ? `${stats.departments_active} / ${stats.departments_total}` : '—'}
            label="有效部门"
          />
          <MobileStatCard
            value={stats ? `${stats.users_active} / ${stats.users_total}` : '—'}
            label="有效员工"
          />
          <MobileStatCard
            value={stats ? `${oauthMatch}%` : '—'}
            label="OAuth 关联"
            sub={stats ? `${stats.linked_directory_users} / ${stats.oauth_users}` : undefined}
          />
          <MobileStatCard
            value={runs[0] ? (STATUS_LABEL[runs[0].status] ?? runs[0].status) : '未执行'}
            label="最近同步"
          />
        </div>
        {stats && stats.users_resigned > 0 && (
          <Alert
            type="info"
            showIcon
            style={{ marginTop: 12 }}
            message={`目录中有 ${stats.users_resigned} 名离职员工，ACL 已自动排除。`}
          />
        )}
      </MobileSection>

      {ENTERPRISE_SECTIONS.map((section) => (
        <MobileSettingsGroup key={section.title} title={section.title}>
          {section.items.map((item) => (
            <MobileSettingsRow
              key={item.key}
              icon={item.icon}
              title={item.label}
              onClick={() => navigate(pagePath(item.key))}
            />
          ))}
        </MobileSettingsGroup>
      ))}
    </MobilePage>
  );
}
