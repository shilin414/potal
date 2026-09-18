/**
 * MobileEntityRow — the rich list row every mobile centre shares
 * (开发执行报告 §14, 二次复审 P2-1/P3-2).
 *
 * Wrapper / Main Button / Favorite Button / More Button — every independent
 * action is a REAL native <button> (no span role="button", no button nested
 * inside a button): the browser owns the focus tree, Enter/Space activation
 * and screen-reader semantics, and stopPropagation hacks disappear.
 * The wrapper keeps the hairline-divider look; `.mobile-console-row__main`
 * owns avatar / title / description / meta.
 *
 * The trailing chevron lives INSIDE the main button (P3-2): it is a plain
 * icon (not a nested interactive element), and users read tapping “>” as
 * entering the row — the chevron area must be part of the main tap target.
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
  <div className="mobile-console-row">
    <button
      type="button"
      className="mobile-console-row__main"
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
      {statusDot && (
        <span className={`mobile-console-row__status${statusDot === 'on' ? ' mobile-console-row__status--on' : ' mobile-console-row__status--off'}`}>
          <span
            className={`mobile-console-row__dot${statusDot === 'on' ? ' mobile-console-row__dot--on' : ' mobile-console-row__dot--off'}`}
            aria-hidden
          />
          {statusLabel}
        </span>
      )}
      {!onMore && (
        <RightOutlined className="mobile-console-row__chevron" aria-hidden />
      )}
    </button>
    {typeof favorite === 'boolean' && onFavorite && (
      <button
        type="button"
        className={`mobile-console-row__star${favorite ? ' mobile-console-row__star--on' : ''}`}
        aria-label={favorite ? `取消收藏：${title}` : `收藏：${title}`}
        onClick={onFavorite}
      >
        {favorite ? <StarFilled /> : <StarOutlined />}
      </button>
    )}
    {onMore && (
      <button
        type="button"
        className="mobile-console-row__more"
        aria-label={`更多操作：${title}`}
        onClick={onMore}
      >
        •••
      </button>
    )}
  </div>
);

export default MobileEntityRow;
