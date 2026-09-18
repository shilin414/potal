/**
 * MobileSearchBar — the one mobile search field (开发执行报告 §17).
 * 44px tall, full width, elevated background. The search semantics stay with
 * the caller (`useApplicationPage({ query })`): no local filtering.
 */
import React from 'react';
import { Input } from 'antd';
import { SearchOutlined } from '@ant-design/icons';

export interface MobileSearchBarProps {
  placeholder?: string;
  value: string;
  onChange: (value: string) => void;
  autoFocus?: boolean;
  'aria-label'?: string;
}

const MobileSearchBar: React.FC<MobileSearchBarProps> = ({
  placeholder, value, onChange, autoFocus, 'aria-label': ariaLabel,
}) => (
  <div className="mobile-console-search">
    <Input
      allowClear
      autoFocus={autoFocus}
      aria-label={ariaLabel ?? placeholder}
      prefix={<SearchOutlined style={{ color: 'var(--color-text-dim)' }} />}
      placeholder={placeholder}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    />
  </div>
);

export default MobileSearchBar;
