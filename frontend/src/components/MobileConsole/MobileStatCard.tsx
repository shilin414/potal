/**
 * MobileStatCard — the 2-column overview tile for the enterprise home
 * (开发执行报告 §33). value / label / optional subvalue, no AntD Statistic.
 */
import React from 'react';

export interface MobileStatCardProps {
  value: React.ReactNode;
  label: string;
  sub?: string;
}

const MobileStatCard: React.FC<MobileStatCardProps> = ({ value, label, sub }) => (
  <div className="mobile-console-stat">
    <div className="mobile-console-stat__value">{value}</div>
    <div className="mobile-console-stat__label">{label}</div>
    {sub && <div className="mobile-console-stat__sub">{sub}</div>}
  </div>
);

export default MobileStatCard;
