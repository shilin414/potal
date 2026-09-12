/**
 * HomeWorkspace — the idle Main Workspace (§4/§5).
 *
 * It is a Workspace Shell, not a "main agent page": nothing is created until
 * the user actually sends the first message or opens an application, so an
 * idle visit produces no Conversation, no Run and no provider session.
 */
import React, { useEffect, useMemo, useState } from 'react';
import { Spin } from 'antd';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { api } from '@/services/api';
import { RunChatPanel } from '@/components/Chat';
import type { SendDecision } from '@/components/Chat/RunChatPanel';
import { useApplicationCatalogStore, resolveDefaultApplication } from '@/stores/useApplicationCatalogStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { routeMention } from '@/lib/mentionRouter';
import type { V2Application } from '@/services/runApi';
import HomeShortcuts from './HomeShortcuts';
import './HomeWorkspace.css';

const HomeWorkspace: React.FC = () => {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const applications = useApplicationCatalogStore((state) => state.applications);
  const catalogLoading = useApplicationCatalogStore((state) => state.isLoading);
  const toggleFavorite = useApplicationCatalogStore((state) => state.toggleFavorite);
  const load = useApplicationCatalogStore((state) => state.load);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const rememberConversation = useWorkspaceStore((state) => state.rememberConversation);
  const queuePrompt = useWorkspaceStore((state) => state.queuePrompt);
  const [resolving, setResolving] = useState(false);

  useEffect(() => { void load(); }, [load]);

  // The composer needs a binding to send through; the main agent (§38) plays
  // the configurable "default main agent" role.
  const defaultApplication = useMemo(
    () => resolveDefaultApplication(applications), [applications]);

  const paramConversation = searchParams.get('conversation');
  const conversationParam = paramConversation
    && Number.isInteger(Number(paramConversation)) ? paramConversation : null;

  // `/?conversation=N` is the history-sidebar deep link. It carries no
  // application, so resolve the owning application and hand over to the real
  // chat workspace — the home workspace itself never renders history.
  useEffect(() => {
    if (!conversationParam) return undefined;
    // The catalog may still be loading on a cold deep-link: the "no
    // application" branch must only run once the catalog is actually
    // loaded, otherwise the ?conversation param is dropped on a race
    // (manifested when the conversations API got faster than the catalog).
    if (catalogLoading) return undefined;
    let active = true;
    setResolving(true);
    api.get<any>(`/conversations/${conversationParam}/`)
      .then((detail) => {
        if (!active) return;
        const target = applications.find(
          (app) => app.id === detail?.application_id);
        if (target) {
          openApplication(target.id);
          rememberConversation(target.id, Number(conversationParam));
          navigate(`/chat/${target.slug}?conversation=${conversationParam}`,
            { replace: true });
          return;
        }
        // No application (legacy conversation): drop back to the idle shell.
        setSearchParams({}, { replace: true });
      })
      .catch(() => {
        if (active) setSearchParams({}, { replace: true });
      })
      .finally(() => { if (active) setResolving(false); });
    return () => { active = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conversationParam, applications, catalogLoading]);

  const candidates = useMemo(() => applications.map((app) => ({
    id: app.id, slug: app.slug, name: app.name, kind: app.kind,
  })), [applications]);

  const handleRouteSend = (raw: string): SendDecision => {
    const decision = routeMention({
      text: raw, candidates, activeApplicationId: defaultApplication?.id ?? null,
    });
    switch (decision.action) {
      case 'passthrough':
        return { action: 'send', content: raw };
      case 'send':
        return { action: 'send', content: decision.content };
      case 'ignore':
        return { action: 'ignore' };
      case 'open': {
        const target = applications.find((app) => app.id === decision.applicationId);
        if (!target) return { action: 'send', content: raw };
        navigate(`/app/${target.slug}`);
        return { action: 'handled' };
      }
      case 'switch': {
        const target = applications.find((app) => app.id === decision.applicationId);
        if (!target) return { action: 'send', content: raw };
        if (decision.content) queuePrompt(target.id, decision.content);
        openApplication(target.id);
        navigate(`/chat/${target.slug}`);
        return { action: 'handled' };
      }
      default:
        return { action: 'send', content: raw };
    }
  };

  const openApplicationWorkspace = (application: V2Application) => {
    openApplication(application.id);
    navigate(application.kind === 'chat'
      ? `/chat/${application.slug}`
      : `/app/${application.slug}`);
  };

  if (resolving) {
    return (
      <div className="workspace-host__loading"><Spin size="large" /></div>
    );
  }

  return (
    <div className="workspace-host">
      <RunChatPanel
        applicationId={defaultApplication?.id}
        application={defaultApplication}
        conversationId={null}
        onRouteSend={handleRouteSend}
        title="今天想做什么？"
        description="选择一个智能体，或直接输入你的需求开始。"
        emptyState={(
          <div className="home-workspace">
            <div className="home-workspace__hero">
              <h2 className="home-workspace__title">今天想做什么？</h2>
              <p className="home-workspace__subtitle">
                选择一个智能体继续，或直接在下方输入你的需求。
              </p>
            </div>
            <HomeShortcuts
              applications={applications}
              onOpen={openApplicationWorkspace}
              onToggleFavorite={toggleFavorite}
            />
          </div>
        )}
        onConversationCreated={(id) => {
          if (!defaultApplication) return;
          rememberConversation(defaultApplication.id, id);
          navigate(`/chat/${defaultApplication.slug}?conversation=${id}`,
            { replace: true });
        }}
      />
    </div>
  );
};

export default HomeWorkspace;
