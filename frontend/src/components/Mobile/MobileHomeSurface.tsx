/**
 * MobileHomeSurface — the idle mobile home (design report §3).
 *
 * Replaces the desktop `HomeShortcuts` wall ONLY on mobile (§27): the desktop
 * component keeps its 收藏/常用/推荐 groups, while mobile collapses to an
 * Agent-first surface whose whole job is 三件事 (§2):
 *
 *   1. be the default agent's blank-task entry;
 *   2. switch to another agent quickly;
 *   3. open a fixed application quickly.
 *
 * 管理 surfaces (智能体市场 / 应用中心) stay in the drawer and are NOT the
 * primary path here (§14): the sheet is for USE, the market is for MANAGEMENT.
 * The two are not interchangeable, which is why picking from the sheet is a
 * plain `onSelect` → navigate instead of a link into the market.
 *
 * The report asks that 首屏 show 头像 + Hero + 双入口 + Composer WITHOUT
 * scrolling (§21). That is achieved structurally rather than by fixed sizes:
 * the composer is a sibling of this surface inside the workspace column, so
 * this component only ever fills the space above it.
 */
import React, { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { resolveDefaultApplication, useApplicationCatalogStore } from '@/stores/useApplicationCatalogStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { routeForApplication } from '@/lib/applicationRoute';
import type { V2Application } from '@/services/runApi';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import MobileCatalogSheet from './MobileCatalogSheet';
import './MobileHomeSurface.css';

const MobileHomeSurface: React.FC = () => {
  const navigate = useNavigate();
  const applications = useApplicationCatalogStore((state) => state.applications);
  const catalogLoading = useApplicationCatalogStore((state) => state.isLoading);
  const recentApplicationIds = useWorkspaceStore((state) => state.recentApplicationIds);
  const openApplication = useWorkspaceStore((state) => state.openApplication);

  const [sheet, setSheet] = useState<'agent' | 'app' | null>(null);

  const defaultApplication = useMemo(
    () => resolveDefaultApplication(applications), [applications]);

  const open = (application: V2Application) => {
    // Same contract as the desktop shortcuts: mark it active (which also
    // records local recency) and let the route pick the right renderer.
    openApplication(application.id);
    navigate(routeForApplication(application));
  };

  return (
    <div className="mobile-home">
      <div className="mobile-home__hero">
        {/* The default agent's face is the visual anchor of the surface (§3.2):
            it tells the user who will answer before they type. */}
        <AgentAvatar
          application={defaultApplication}
          size={64}
          shape="circle"
          className="mobile-home__avatar"
          tint={defaultApplication?.color}
        />
        <h2 className="mobile-home__title">今天想做什么？</h2>
      </div>

      {/* Two catalogue entries, not two content panes (§4.1): each opens the
          SAME sheet component against a different slice of the catalog. */}
      <div className="mobile-home__entries">
        <button
          type="button"
          className="mobile-home__entry"
          onClick={() => setSheet('agent')}
        >
          智能体
        </button>
        <span className="mobile-home__entries-sep" aria-hidden />
        <button
          type="button"
          className="mobile-home__entry"
          onClick={() => setSheet('app')}
        >
          应用
        </button>
      </div>

      <MobileCatalogSheet
        open={sheet === 'agent'}
        type="agent"
        applications={applications}
        recentIds={recentApplicationIds}
        activeApplicationId={defaultApplication?.id ?? null}
        loading={catalogLoading}
        onClose={() => setSheet(null)}
        onSelect={open}
      />
      <MobileCatalogSheet
        open={sheet === 'app'}
        type="app"
        applications={applications}
        recentIds={recentApplicationIds}
        activeApplicationId={null}
        loading={catalogLoading}
        onClose={() => setSheet(null)}
        onSelect={open}
      />
    </div>
  );
};

export default MobileHomeSurface;
