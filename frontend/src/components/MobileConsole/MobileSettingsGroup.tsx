/**
 * MobileSettingsGroup / MobileSettingsRow — the menu hub rows of the
 * enterprise home (开发执行报告 §32/§34): icon / title / description /
 * chevron, reusable by any future settings page.
 */
import React from 'react';
import { RightOutlined } from '@ant-design/icons';
import MobileSection from './MobileSection';

export interface MobileSettingsRowProps {
  icon: React.ReactNode;
  title: string;
  description?: string;
  onClick: () => void;
}

export const MobileSettingsRow: React.FC<MobileSettingsRowProps> = ({
  icon, title, description, onClick,
}) => (
  <button type="button" className="mobile-console-settings-row" onClick={onClick}>
    <span className="mobile-console-settings-row__icon">{icon}</span>
    <span className="mobile-console-settings-row__body">
      <span className="mobile-console-settings-row__title">{title}</span>
      {description && (
        <span className="mobile-console-settings-row__desc">{description}</span>
      )}
    </span>
    <RightOutlined className="mobile-console-settings-row__chevron" />
  </button>
);

export interface MobileSettingsGroupProps {
  title?: string;
  children: React.ReactNode;
}

const MobileSettingsGroup: React.FC<MobileSettingsGroupProps> = ({ title, children }) => (
  <MobileSection title={title} flush>
    {children}
  </MobileSection>
);

export default MobileSettingsGroup;
