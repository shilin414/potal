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
import React, { useEffect, useState } from 'react';
import { Spin, message } from 'antd';
import { DownOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { routeForApplication } from '@/lib/applicationRoute';
import type { ApplicationSummary } from '@/services/runApi';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import MobileCatalogSheet from './MobileCatalogSheet';
import './MobileAgentSwitcher.css';

interface Props {
  /** The agent the current route is showing, when it knows one. */
  activeApplicationId?: number | null;
}

const MobileAgentSwitcher: React.FC<Props> = ({ activeApplicationId }) => {
  const navigate = useNavigate();
  // The top bar needs the ACTIVE agent's name + face. That is one entity —
  // looked up in the application entity cache and, on a miss, resolved by id
  // (执行报告 §9.4/§12). It used to arrive as a side effect of the shell
  // downloading the whole catalog.
  const entities = useApplicationEntityStore((state) => state.byId);
  const ensure = useApplicationEntityStore((state) => state.ensure);
  const loadBootstrap = useWorkspaceBootstrapStore((state) => state.load);
  const storeActiveId = useWorkspaceStore((state) => state.activeApplicationId);
  const recentApplicationIds = useWorkspaceStore((state) => state.recentApplicationIds);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const [open, setOpen] = useState(false);
  const [resolving, setResolving] = useState(false);

  useEffect(() => { void loadBootstrap(); }, [loadBootstrap]);

  // The route wins when it names an agent (a deep link into /chat/:slug); the
  // workspace store is the fallback for the idle home surface.
  const effectiveActiveId = activeApplicationId ?? storeActiveId;
  const active = effectiveActiveId != null ? entities[effectiveActiveId] : undefined;

  useEffect(() => {
    if (effectiveActiveId == null) return undefined;
    if (entities[effectiveActiveId]) return undefined;
    let live = true;
    setResolving(true);
    void ensure(effectiveActiveId).finally(() => { if (live) setResolving(false); });
    return () => { live = false; };
  }, [effectiveActiveId, entities, ensure]);

  const loading = resolving && !active;

  const handleSelect = (application: ApplicationSummary) => {
    void ensure(application.id, { maxAgeMs: 0 })
      .then((current) => {
        if (!current) {
          message.error('应用当前不可用');
          return;
        }
        openApplication(current.id);
        navigate(routeForApplication(current));
      })
      .catch(() => {
        // Transport failure: let WorkspaceHost own the retry state.
        openApplication(application.id);
        navigate(routeForApplication(application));
      });
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
        activeApplication={active ?? null}
        onClose={() => setOpen(false)}
        onSelect={handleSelect}
      />
    </>
  );
};

export default MobileAgentSwitcher;
