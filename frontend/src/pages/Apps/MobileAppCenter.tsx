/**
 * MobileAppCenter — the mobile 应用中心 (开发执行报告 §18–§20).
 *
 * Shares MobileCatalogContent with the agent centre (80%+ reuse, §71): no
 * 130px thumbnails here — a 44px tinted icon, name, description and
 * 分类 · 类型 meta per row. Same consume-mode paged endpoint, same open
 * contract as the picker sheet.
 */
import React from 'react';
import { useNavigate } from 'react-router-dom';
import { routeForApplication } from '@/lib/applicationRoute';
import type { ApplicationSummary } from '@/services/runApi';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { MobilePage } from '@/components/MobileConsole';
import MobileCatalogContent from '@/components/Mobile/MobileCatalogContent';

const MobileAppCenter: React.FC = () => {
  const navigate = useNavigate();
  const recentIds = useWorkspaceStore((state) => state.recentApplicationIds);
  const openApplication = useWorkspaceStore((state) => state.openApplication);

  const open = (application: ApplicationSummary) => {
    openApplication(application.id);
    navigate(routeForApplication(application));
  };

  return (
    <MobilePage>
      <MobileCatalogContent
        type="app"
        mode="page"
        recentIds={recentIds}
        onSelect={open}
      />
    </MobilePage>
  );
};

export default MobileAppCenter;
