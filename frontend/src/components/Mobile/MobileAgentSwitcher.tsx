/**
 * MobileAgentSwitcher — the mobile top bar's agent entry (design report §7).
 *
 * It replaces `<ApplicationSwitcher compact />`, whose Ant Design Dropdown is a
 * DESKTOP interaction (§7.1). Both the home surface and the chat top bar now
 * open the same `MobileCatalogSheet(type="agent")`, so "pick an agent" is one
 * component and one gesture everywhere (§7.1, §20).
 *
 * The sheet itself fetches its rows from the paged endpoint (执行报告 §25) —
 * this component hands it only the ACTIVE application (for the ✓ marker), not
 * a full agent array.
 *
 * `ApplicationSwitcher` itself is left untouched for desktop: the report
 * explicitly warns against threading `if (compact)` through one component to
 * make it serve both a Dropdown and a Bottom Sheet (§7.3).
 */
import React, { useState } from 'react';
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

const MobileAgentSwitcher: React.FC<Props> = ({ activeApplicationId }) => {
  const navigate = useNavigate();
  // The catalog mirror is the lookup for the ACTIVE agent's name/avatar.
  // (The P1 workspace-bootstrap endpoint will replace this whole-catalog
  // load; the picker already reads the paged endpoint directly.)
  const applicationById = useApplicationCatalogStore(
    (state) => state.applicationById);
  const isLoading = useApplicationCatalogStore((state) => state.isLoading);
  const load = useApplicationCatalogStore((state) => state.load);
  const storeActiveId = useWorkspaceStore((state) => state.activeApplicationId);
  const recentApplicationIds = useWorkspaceStore((state) => state.recentApplicationIds);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const [open, setOpen] = useState(false);

  React.useEffect(() => { void load(); }, [load]);

  // The route wins when it names an agent (a deep link into /chat/:slug); the
  // workspace store is the fallback for the idle home surface.
  const effectiveActiveId = activeApplicationId ?? storeActiveId;

  const active = applicationById(effectiveActiveId) || null;

  const loading = isLoading;

  const handleSelect = (application: V2Application) => {
    openApplication(application.id);
    navigate(routeForApplication(application));
  };

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
        recentIds={recentApplicationIds}
        activeApplicationId={effectiveActiveId}
        activeApplication={active}
        onClose={() => setOpen(false)}
        onSelect={handleSelect}
      />
    </>
  );
};

export default MobileAgentSwitcher;
