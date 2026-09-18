/**
 * MobileActionSheet — the shared ••• actions bottom sheet
 * (开发执行报告 §27/§58). Used by Schedules, enterprise resources and audit.
 * Destructive confirmations stay with the caller (§27: ActionSheet → Modal
 * confirm, not Popconfirm).
 */
import React from 'react';
import { Drawer } from 'antd';
import './MobileConsole.css';

export interface MobileAction {
  key: string;
  label: string;
  icon?: React.ReactNode;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
}

export interface MobileActionSheetProps {
  open: boolean;
  title?: string;
  actions: MobileAction[];
  onClose: () => void;
}

const MobileActionSheet: React.FC<MobileActionSheetProps> = ({
  open, title, actions, onClose,
}) => (
  <Drawer
    placement="bottom"
    open={open}
    onClose={onClose}
    height="auto"
    closable={false}
    title={null}
    rootClassName="mobile-bottom-sheet"
    styles={{
      content: { borderRadius: '24px 24px 0 0' },
      body: { padding: 0, display: 'flex', flexDirection: 'column', minHeight: 0 },
    }}
  >
    <div className="mobile-sheet">
      <div className="mobile-sheet__handle" aria-hidden />
      {title && (
        <div className="mobile-sheet__header">
          <span className="mobile-sheet__title">{title}</span>
        </div>
      )}
      <div className="mobile-action-sheet__list">
        {actions.map((action) => (
          <button
            key={action.key}
            type="button"
            className={`mobile-action-sheet__row${action.danger ? ' mobile-action-sheet__row--danger' : ''}`}
            disabled={action.disabled}
            onClick={() => {
              onClose();
              action.onClick();
            }}
          >
            {action.icon && <span className="mobile-action-sheet__row-icon">{action.icon}</span>}
            {action.label}
          </button>
        ))}
      </div>
    </div>
  </Drawer>
);

export default MobileActionSheet;
