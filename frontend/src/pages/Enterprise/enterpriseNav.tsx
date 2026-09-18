/**
 * enterpriseNav — the ONE navigation map of the enterprise console
 * (开发执行报告 §88): Desktop renders it as the Sider Menu, Mobile renders
 * it as the home menu hub, but the keys — and therefore the URLs — are
 * identical, so 刷新 / 分享 / 前进后退 behave the same on both.
 */
import React from 'react';
import {
  ApartmentOutlined,
  AppstoreOutlined,
  AuditOutlined,
  CloudSyncOutlined,
  RobotOutlined,
  SafetyCertificateOutlined,
  TeamOutlined,
} from '@ant-design/icons';

export interface EnterpriseNavItem {
  /** URL path segment under /enterprise/ (may carry a query, e.g. access?app=). */
  key: string;
  label: string;
  icon: React.ReactNode;
}

export interface EnterpriseNavSection {
  title: string;
  items: EnterpriseNavItem[];
}

export const ENTERPRISE_SECTIONS: EnterpriseNavSection[] = [
  {
    title: '资源管理',
    items: [
      { key: 'resources/agents', label: '智能体管理', icon: <RobotOutlined /> },
      { key: 'resources/apps', label: '应用管理', icon: <AppstoreOutlined /> },
    ],
  },
  {
    title: '权限管理',
    items: [
      { key: 'access/agents', label: '智能体授权', icon: <SafetyCertificateOutlined /> },
      { key: 'access/apps', label: '应用授权', icon: <SafetyCertificateOutlined /> },
    ],
  },
  {
    title: '组织架构',
    items: [
      { key: 'directory', label: '部门与人员', icon: <TeamOutlined /> },
      { key: 'directory/sync', label: '同步管理', icon: <CloudSyncOutlined /> },
    ],
  },
  {
    title: '平台管理',
    items: [
      { key: 'providers', label: 'Provider', icon: <ApartmentOutlined /> },
      { key: 'audit', label: '审计日志', icon: <AuditOutlined /> },
    ],
  },
];

export const pagePath = (key: string) => `/enterprise/${key}`;

export const currentKey = (pathname: string) =>
  pathname.replace(/^\/enterprise\/?/, '') || 'overview';

export const fmt = (v?: string | null) => (v ? new Date(v).toLocaleString() : '—');
