/**
 * MobileEnterpriseConsole — 企业控制台移动端（开发执行报告 §31/§52/§88）。
 *
 * 按 pathname 切换子页面（URL 与桌面完全一致，不引入 ?tab=）。二级页面
 * 通过 useMobileHeader 把顶栏换成「← 标题」，返回固定回 /enterprise，
 * 避免浏览器历史混乱（§5 企业控制台二级页面）。
 */
import React, { useMemo } from 'react';
import { useLocation } from 'react-router-dom';
import { useMobileHeader } from '@/shell/mobileHeader';
import { currentKey } from '../enterpriseNav';
import MobileEnterpriseHome from './MobileEnterpriseHome';
import MobileResourcePage from './MobileResourcePage';
import MobileAccessPage from './MobileAccessPage';
import MobileDirectoryPage from './MobileDirectoryPage';
import MobileSyncPage from './MobileSyncPage';
import MobileProvidersPage from './MobileProvidersPage';
import MobileAuditPage from './MobileAuditPage';

const SUBPAGE_TITLES: Record<string, string> = {
  'resources/agents': '智能体管理',
  'resources/apps': '应用管理',
  'access/agents': '智能体授权',
  'access/apps': '应用授权',
  'directory': '部门与人员',
  'directory/sync': '同步管理',
  'providers': 'Provider',
  'audit': '审计日志',
};

export default function MobileEnterpriseConsole() {
  const location = useLocation();
  const key = currentKey(location.pathname);
  const baseKey = key.split('?')[0];

  const isHome = baseKey === 'overview' || baseKey === '';
  const title = SUBPAGE_TITLES[baseKey];

  // 首页沿用路由 handle 的 console 模式；二级页面覆盖为 detail（← 返回）。
  useMobileHeader(isHome || !title ? null : {
    mode: 'detail',
    title,
    backTo: '/enterprise',
  });

  // key：resources/agents ↔ resources/apps 是同型组件，不加 key 会复用实例，
  // 搜索词/编辑器开合等状态跨 kind 泄漏。
  const content = useMemo(() => {
    if (baseKey === 'resources/agents') return <MobileResourcePage key={baseKey} kind="chat" />;
    if (baseKey === 'resources/apps') return <MobileResourcePage key={baseKey} kind="fixed" />;
    if (baseKey.startsWith('access/agents')) return <MobileAccessPage key={baseKey} kind="chat" />;
    if (baseKey.startsWith('access/apps')) return <MobileAccessPage key={baseKey} kind="fixed" />;
    if (baseKey === 'directory') return <MobileDirectoryPage key={baseKey} />;
    if (baseKey === 'directory/sync') return <MobileSyncPage key={baseKey} />;
    if (baseKey === 'providers') return <MobileProvidersPage key={baseKey} />;
    if (baseKey === 'audit') return <MobileAuditPage key={baseKey} />;
    return <MobileEnterpriseHome key="overview" />;
  }, [baseKey]);

  return <div className="enterprise-console-mobile">{content}</div>;
}
