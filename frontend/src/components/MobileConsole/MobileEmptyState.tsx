/**
 * MobileEmptyState — the shared empty/error tail of the mobile centres
 * (开发执行报告 §62): a title, a hint, and an optional action (重试 / 创建).
 */
import React from 'react';

export interface MobileEmptyStateProps {
  title: string;
  hint?: string;
  action?: React.ReactNode;
}

const MobileEmptyState: React.FC<MobileEmptyStateProps> = ({ title, hint, action }) => (
  <div className="mobile-console-empty">
    <strong>{title}</strong>
    {hint && <span>{hint}</span>}
    {action}
  </div>
);

export default MobileEmptyState;
