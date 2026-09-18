/**
 * MobileCatalogSheet — the mobile 智能体 / 应用 picker (design report §4–§6).
 *
 * ONE component serves both pickers and every entry point (home 双入口, the
 * chat top bar, the desktop switcher's mobile counterpart), because the report
 * is explicit that they are two *catalogue entries*, not two content panes
 * (§4.1): the difference is only which slice of the catalog shows and what the
 * title says.
 *
 * Since the Mobile Console refactor (开发执行报告 §21/§22) this is a THIN
 * SHELL: the Drawer chrome (bottom placement, rounded top, grab handle) plus
 * `MobileCatalogContent`, which owns every browsing rule the sheet's tests
 * pin — server paging, 最近使用, category rails, active ✓, empty states.
 * The centres (MobileAgentCenter / MobileAppCenter) render the very same
 * content component in `mode="page"`, so picker and page can never drift.
 *
 * What it deliberately is NOT:
 *   · not a route — picking an item returns the application to the caller;
 *   · not a management surface — Provider / Runtime / 编辑 / 删除 stay in
 *     the centres (§5.5);
 *   · not the desktop Dropdown — that is `ApplicationSwitcher`, untouched.
 */
import React from 'react';
import { Drawer } from 'antd';
import type { ApplicationSummary, V2Application } from '@/services/runApi';
import MobileCatalogContent from './MobileCatalogContent';
import './MobileSheets.css';

export interface MobileCatalogSheetProps {
  open: boolean;
  type: 'agent' | 'app';
  /**
   * Legacy local pool. UNDEFINED in production — the sheet then pages the
   * server itself; unit tests pass a pool to stay request-free.
   */
  applications?: V2Application[];
  /** The application in play right now — marked with ✓ (§5.5). */
  activeApplicationId?: number | null;
  /**
   * Server-paged mode only: the object for `activeApplicationId`, injected
   * by the caller so the tick renders even when the active row is not on a
   * fetched page (e.g. an unbound agent the list legitimately omits).
   */
  activeApplication?: ApplicationSummary | null;
  recentIds: number[];
  /** Show row skeletons instead of an 空列表 while loading (§15.1). */
  loading?: boolean;
  onClose: () => void;
  onSelect: (application: ApplicationSummary) => void;
}

const MobileCatalogSheet: React.FC<MobileCatalogSheetProps> = ({
  open,
  type,
  applications,
  activeApplicationId,
  activeApplication,
  recentIds,
  loading,
  onClose,
  onSelect,
}) => (
  <Drawer
    placement="bottom"
    open={open}
    onClose={onClose}
    height="82dvh"
    closable={false}
    title={null}
    rootClassName="mobile-bottom-sheet"
    // 22–24px top radius (§13.1). Set inline as well as in CSS so the shape
    // survives antd version differences in the content class path.
    styles={{
      content: { borderRadius: '24px 24px 0 0' },
      body: { padding: 0, display: 'flex', flexDirection: 'column', minHeight: 0 },
    }}
  >
    <div className="mobile-sheet">
      <div className="mobile-sheet__handle" aria-hidden />
      <MobileCatalogContent
        type={type}
        mode="sheet"
        active={open}
        applications={applications}
        activeApplicationId={activeApplicationId}
        activeApplication={activeApplication}
        recentIds={recentIds}
        loading={loading}
        onClose={onClose}
        onSelect={onSelect}
      />
    </div>
  </Drawer>
);

export default MobileCatalogSheet;
