/**
 * MobileSection — titled block inside a MobilePage (开发执行报告 §8/§10).
 * `flush` renders rows as one continuous card list with hairline dividers
 * (§14: 白底页面 + 极浅 divider, not one card per row).
 */
import React from 'react';

export interface MobileSectionProps {
  title?: string;
  description?: string;
  /** Wrap direct children in the shared rows card (dividers between rows). */
  flush?: boolean;
  children: React.ReactNode;
}

const MobileSection: React.FC<MobileSectionProps> = ({
  title, description, flush = false, children,
}) => (
  <section className="mobile-console-section">
    {title && <h2 className="mobile-console-section__title">{title}</h2>}
    {description && <p className="mobile-console-section__desc">{description}</p>}
    {flush ? (
      <div className="mobile-console-section__rows">{children}</div>
    ) : children}
  </section>
);

export default MobileSection;
