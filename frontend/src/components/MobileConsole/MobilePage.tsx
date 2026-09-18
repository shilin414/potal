/**
 * MobilePage — the standard container for every mobile console page
 * (开发执行报告 §8/§61/§94).
 *
 * Owns the ONE scroll container of the route: the shell's <main> stays
 * `overflow: hidden` and this page scrolls, so a bottom sheet or full-screen
 * drawer on top never fights a second scroller. The page title lives in the
 * shell header, NOT here — the page starts with its controls or content.
 */
import React from 'react';

export interface MobilePageProps {
  children: React.ReactNode;
  className?: string;
}

const MobilePage: React.FC<MobilePageProps> = ({ children, className }) => (
  <div className={`mobile-console-page${className ? ` ${className}` : ''}`}>
    {children}
  </div>
);

export default MobilePage;
