/**
 * MobileAgentSwitcher — the mobile top bar's agent entry (design report §7).
 *
 * It replaces `<ApplicationSwitcher compact />`, whose Ant Design Dropdown is a
 * DESKTOP interaction (§7.1). Both the home surface and the chat top bar now
 * open the same `MobileCatalogSheet(type="agent")`, so "pick an agent" is one
 * component and one gesture everywhere (§7.1, §20).
 *
 * `ApplicationSwitcher` itself is left untouched for desktop: the report
 * explicitly warns against threading `if (compact)` through one component to
 * make it serve both a Dropdown and a Bottom Sheet (§7.3).
 */
import React, { useEffect, useState } from 'react';
import { Spin } from 'antd';
import { DownOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useApplicationCatalogStore } from '@/stores/useApplicationCatalogStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { routeForApplication } from '@/lib/applicationRoute';
import type { V2Application } from '@/services/runApi';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import MobileCatalogSheet from './MobileCatalogSheet';
import './MobileAgentSwitcher.css';

interface Props {
  /** The agent the current route is showing, when it knows one. */
  activeApplicationId?: number | null;
}

/**
 * The list handed to the sheet, order-preserving and deduplicated by id.
 *
 * `chats` is `chatApplicationsList` and `active` is looked up inside it, so
 * the two are normally the same array and this returns it UNTOUCHED — which
 * matters, because it keeps the sheet's `useMemo`s stable across renders. The
 * copy path only runs for the one edge case it exists for: the route names an
 * agent the catalog has not (re)loaded yet, so the sheet would otherwise show
 * a list without the agent currently on screen.
 */
function applicationsForSheet(
  chats: V2Application[],
  active: V2Application | null,
): V2Application[] {
  if (!active || chats.some((app) => app.id === active.id)) return chats;
  return [active, ...chats];
}

const MobileAgentSwitcher: React.FC<Props> = ({ activeApplicationId }) => {
  const navigate = useNavigate();
  // Assigned to a short local name on purpose: this is the SELECTOR, so the
  // store only notifies this component when the derived array is replaced,
  // not on every unrelated update (favourites, load flags, …).
  const chats = useApplicationCatalogStore((state) => state.chatApplicationsList);
  const isLoading = useApplicationCatalogStore((state) => state.isLoading);
  const load = useApplicationCatalogStore((state) => state.load);
  const storeActiveId = useWorkspaceStore((state) => state.activeApplicationId);
  const recentApplicationIds = useWorkspaceStore((state) => state.recentApplicationIds);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const [open, setOpen] = useState(false);

  useEffect(() => { void load(); }, [load]);

  // The route wins when it names an agent (a deep link into /chat/:slug); the
  // workspace store is the fallback for the idle home surface.
  const effectiveActiveId = activeApplicationId ?? storeActiveId;

  const active = chats.find((app) => app.id === effectiveActiveId) || null;

  const loading = isLoading && !chats.length;

  const handleSelect = (application: V2Application) => {
    openApplication(application.id);
    navigate(routeForApplication(application));
  };

  const sheetApplications = applicationsForSheet(chats, active);

  return (
    <>
      <button
        type="button"
        className="mobile-agent-switcher"
        aria-label="切换智能体"
        disabled={loading}
        onClick={() => setOpen(true)}
      >
        {loading ? (
          <Spin size="small" />
        ) : (
          <>
            {active ? (
              <AgentAvatar
                application={active}
                size={20}
                shape="circle"
                className="mobile-agent-switcher__icon"
              />
            ) : (
              <span className="mobile-agent-switcher__icon">✦</span>
            )}
            <span className="mobile-agent-switcher__name">
              {active?.name || '选择智能体'}
            </span>
            <DownOutlined className="mobile-agent-switcher__caret" />
          </>
        )}
      </button>

      <MobileCatalogSheet
        open={open}
        type="agent"
        applications={sheetApplications}
        recentIds={recentApplicationIds}
        activeApplicationId={effectiveActiveId}
        loading={isLoading}
        onClose={() => setOpen(false)}
        onSelect={handleSelect}
      />
    </>
  );
};

export default MobileAgentSwitcher;
