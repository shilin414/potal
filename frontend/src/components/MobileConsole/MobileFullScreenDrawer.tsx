/**
 * MobileFullScreenDrawer — the mobile editor/detail surface
 * (开发执行报告 §28/§29/§93).
 *
 * 100vw × 100dvh (with a 100vh fallback for older WebViews), its own scroll
 * body, sticky header with safe-area-top, and a semantic trailing action
 * (保存) instead of an OK/Cancel footer. At most two layers of these stack
 * with a picker sheet (§92).
 */
import React from 'react';
import { Button, Drawer } from 'antd';
import { ArrowLeftOutlined } from '@ant-design/icons';
import './MobileConsole.css';

export interface MobileFullScreenDrawerProps {
  open: boolean;
  title: string;
  /** Trailing primary action, e.g. 保存 (§28). */
  actionText?: string;
  actionLoading?: boolean;
  /** Disable the action until its surface is ready (二次复审 P2-6). */
  actionDisabled?: boolean;
  onAction?: () => void;
  onClose: () => void;
  children: React.ReactNode;
}

const MobileFullScreenDrawer: React.FC<MobileFullScreenDrawerProps> = ({
  open, title, actionText, actionLoading, actionDisabled, onAction, onClose, children,
}) => (
  <Drawer
    placement="right"
    open={open}
    onClose={onClose}
    width="100vw"
    closable={false}
    title={null}
    rootClassName="mobile-fs-drawer"
    styles={{
      body: { padding: 0, display: 'flex', flexDirection: 'column', minHeight: 0 },
    }}
  >
    <div className="mobile-fs-drawer__inner">
      <header className="mobile-fs-drawer__header">
        <button
          type="button"
          className="mobile-fs-drawer__back"
          aria-label="返回"
          onClick={onClose}
        >
          <ArrowLeftOutlined />
        </button>
        <div className="mobile-fs-drawer__title" title={title}>{title}</div>
        {actionText && onAction ? (
          <Button
            type="primary"
            size="small"
            className="mobile-fs-drawer__action"
            loading={actionLoading}
            disabled={actionDisabled}
            onClick={onAction}
          >
            {actionText}
          </Button>
        ) : <span className="mobile-fs-drawer__spacer" aria-hidden />}
      </header>
      <div className="mobile-fs-drawer__body">{children}</div>
    </div>
  </Drawer>
);

export default MobileFullScreenDrawer;
