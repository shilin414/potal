/**
 * MobileEntityRow — the rich list row every mobile centre shares
 * (开发执行报告 §14). A real <button> (§65) with avatar / title / description /
 * meta, an optional favorite star that stops propagation (§15), an optional
 * badge (默认智能体), and a trailing chevron.
 */
import React from 'react';
import { RightOutlined, StarFilled, StarOutlined } from '@ant-design/icons';

export interface MobileEntityRowProps {
  avatar: React.ReactNode;
  title: string;
  description?: string;
  meta?: string;
  badge?: string;
  favorite?: boolean;
  /** Status dot + label used by enterprise resource rows (§36). */
  statusDot?: 'on' | 'off';
  statusLabel?: string;
  /** Trailing ••• instead of a chevron (enterprise lists, §36/§37). */
  onMore?: () => void;
  onClick: () => void;
  onFavorite?: () => void;
  'aria-label'?: string;
}

const MobileEntityRow: React.FC<MobileEntityRowProps> = ({
  avatar, title, description, meta, badge, favorite, statusDot, statusLabel,
  onMore, onClick, onFavorite, 'aria-label': ariaLabel,
}) => (
  <button
    type="button"
    className="mobile-console-row"
    aria-label={ariaLabel ?? title}
    onClick={onClick}
  >
    <span className="mobile-console-row__avatar">{avatar}</span>
    <span className="mobile-console-row__body">
      <span className="mobile-console-row__title">
        <span>{title}</span>
        {badge && <span className="mobile-console-row__title-badge">{badge}</span>}
      </span>
      {description && <span className="mobile-console-row__desc">{description}</span>}
      {meta && <span className="mobile-console-row__meta">{meta}</span>}
    </span>
    {typeof favorite === 'boolean' && onFavorite && (
      <span
        role="button"
        tabIndex={0}
        className={`mobile-console-row__star${favorite ? ' mobile-console-row__star--on' : ''}`}
        aria-label={favorite ? `取消收藏：${title}` : `收藏：${title}`}
        onClick={(e) => {
          e.stopPropagation();
          onFavorite();
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            e.stopPropagation();
            onFavorite();
          }
        }}
      >
        {favorite ? <StarFilled /> : <StarOutlined />}
      </span>
    )}
    {statusDot && (
      <span className={`mobile-console-row__status${statusDot === 'on' ? ' mobile-console-row__status--on' : ' mobile-console-row__status--off'}`}>
        <span
          className={`mobile-console-row__dot${statusDot === 'on' ? ' mobile-console-row__dot--on' : ' mobile-console-row__dot--off'}`}
          aria-hidden
        />
        {statusLabel}
      </span>
    )}
    {onMore ? (
      <span
        role="button"
        tabIndex={0}
        className="mobile-console-row__more"
        aria-label={`更多操作：${title}`}
        onClick={(e) => {
          e.stopPropagation();
          onMore();
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            e.stopPropagation();
            onMore();
          }
        }}
      >
        •••
      </span>
    ) : (
      <RightOutlined className="mobile-console-row__chevron" />
    )}
  </button>
);

export default MobileEntityRow;
