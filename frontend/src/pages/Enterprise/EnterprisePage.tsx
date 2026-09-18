/**
 * EnterprisePage — Router Adapter（开发执行报告 §52）。
 *
 * Desktop / Mobile 共用 enterpriseApi 与 URL（§87/§88），只在 React 层切换
 * Presentation：桌面 Sider + Table，移动 Console 首页 + 子页面。
 */
import React from 'react';
import { useIsMobile } from '@/shell/useIsMobile';
import DesktopEnterpriseConsole from './desktop/DesktopEnterpriseConsole';
import MobileEnterpriseConsole from './mobile/MobileEnterpriseConsole';
import './EnterprisePage.css';

export default function EnterprisePage() {
  const isMobile = useIsMobile();
  return isMobile ? <MobileEnterpriseConsole /> : <DesktopEnterpriseConsole />;
}
