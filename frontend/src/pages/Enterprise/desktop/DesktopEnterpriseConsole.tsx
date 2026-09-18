/**
 * DesktopEnterpriseConsole — the desktop console exactly as before the
 * mobile split (开发执行报告 §31/§51/§54): AntD Layout + 220px Sider + Menu,
 * content routed by the shared `currentKey`. Logic moved verbatim from
 * EnterprisePage.tsx; Desktop 不允许回归.
 */
import React, { useMemo } from 'react';
import { Layout, Menu } from 'antd';
import { ControlOutlined } from '@ant-design/icons';
import { useLocation, useNavigate } from 'react-router-dom';
import { ENTERPRISE_SECTIONS, currentKey, pagePath } from '../enterpriseNav';
import Overview from './Overview';
import ResourcePage from './ResourcePage';
import AccessPage from './AccessPage';
import DirectoryPage from './DirectoryPage';
import SyncPage from './SyncPage';
import ProvidersPage from './ProvidersPage';
import AuditPage from './AuditPage';
import '../EnterprisePage.css';

const { Sider, Content } = Layout;

export default function DesktopEnterpriseConsole() {
  const location = useLocation();
  const navigate = useNavigate();
  const key = currentKey(location.pathname);
  const content = useMemo(() => {
    if (key === 'resources/agents') return <ResourcePage kind="chat" />;
    if (key === 'resources/apps') return <ResourcePage kind="fixed" />;
    if (key.startsWith('access/agents')) return <AccessPage kind="chat" />;
    if (key.startsWith('access/apps')) return <AccessPage kind="fixed" />;
    if (key === 'directory') return <DirectoryPage />;
    if (key === 'directory/sync') return <SyncPage />;
    if (key === 'providers') return <ProvidersPage />;
    if (key === 'audit') return <AuditPage />;
    return <Overview />;
  }, [key]);
  return (
    <Layout className="enterprise-console">
      <Sider width={220} theme="light" className="enterprise-sider">
        <div className="enterprise-brand">企业控制台</div>
        <Menu
          mode="inline"
          selectedKeys={[key.split('?')[0]]}
          items={[
            { key: 'overview', icon: <ControlOutlined />, label: '概览' },
            ...ENTERPRISE_SECTIONS.map((section) => ({
              type: 'group' as const,
              label: section.title,
              children: section.items.map(({ key: k, icon, label }) => ({
                key: k,
                icon,
                label,
              })),
            })),
          ]}
          onClick={({ key: k }) => navigate(pagePath(k))}
        />
      </Sider>
      <Content className="enterprise-content">{content}</Content>
    </Layout>
  );
}
