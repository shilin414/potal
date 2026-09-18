/**
 * MobileAgentCenter — the mobile 智能体中心 (开发执行报告 §11–§16).
 *
 * Same business layer as the desktop grid (`useApplicationPage` consume mode,
 * `setApplicationFavorite`, the workspace bootstrap's rails), a different
 * information architecture: search + category rail + 最近使用 + rich rows.
 * The browsing itself is delegated to MobileCatalogContent so the picker
 * sheet and this page can never drift (§21).
 */
import React from 'react';
import { useNavigate } from 'react-router-dom';
import { message } from 'antd';
import { routeForApplication } from '@/lib/applicationRoute';
import { applicationConsumeBlock } from '@/lib/applicationConsumability';
import type { ApplicationSummary, V2Application } from '@/services/runApi';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { MobilePage } from '@/components/MobileConsole';
import MobileCatalogContent from '@/components/Mobile/MobileCatalogContent';

const MobileAgentCenter: React.FC = () => {
  const navigate = useNavigate();
  const recentIds = useWorkspaceStore((state) => state.recentApplicationIds);
  const openApplication = useWorkspaceStore((state) => state.openApplication);

  // Same contract as the picker sheets: block a 停用/未绑定 agent with a
  // warning (desktop grid parity), else record recency and route.
  const open = (application: ApplicationSummary) => {
    const block = applicationConsumeBlock(application as V2Application);
    if (block) {
      message.warning(block);
      return;
    }
    openApplication(application.id);
    navigate(routeForApplication(application));
  };

  return (
    <MobilePage>
      <MobileCatalogContent
        type="agent"
        mode="page"
        recentIds={recentIds}
        favorites
        onSelect={open}
      />
    </MobilePage>
  );
};

export default MobileAgentCenter;
