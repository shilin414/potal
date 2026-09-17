/**
 * HomeWorkspace — the idle Main Workspace (§4/§5).
 *
 * It is a Workspace Shell, not a "main agent page": nothing is created until
 * the user actually sends the first message or opens an application, so an
 * idle visit produces no Conversation, no Run and no provider session.
 *
 * Data (执行报告 §9.2/§13/§14, P1-1/P1-2): the default agent and the shortcut
 * groups come from the constant-size workspace bootstrap, a conversation deep
 * link resolves ONE application by id, and `@mention` routing asks the server
 * for its candidates. None of those needs the whole catalog any more.
 */
import React, { useEffect, useState } from 'react';
import { Spin } from 'antd';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { api } from '@/services/api';
import { RunChatPanel } from '@/components/Chat';
import type { SendDecision } from '@/components/Chat/RunChatPanel';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { routeComposerText } from '@/lib/composerRouting';
import { useIsMobile } from '@/shell/useIsMobile';
import { MobileHomeSurface } from '@/components/Mobile';
import type { ApplicationSummary } from '@/services/runApi';
import HomeShortcuts from './HomeShortcuts';
import './HomeWorkspace.css';

const HomeWorkspace: React.FC = () => {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  // Mobile swaps ONLY the empty state (§6.1): the WorkspaceHost, the Run API
  // and this component's deep-link / @mention handling are shared, so the two
  // shells can never drift on what a shortcut does — only on how it looks.
  const isMobile = useIsMobile();
  const bootstrapLoading = useWorkspaceBootstrapStore((state) => state.isLoading);
  const loadBootstrap = useWorkspaceBootstrapStore((state) => state.load);
  const toggleFavorite = useWorkspaceBootstrapStore((state) => state.toggleFavorite);
  const defaultApplication = useWorkspaceBootstrapStore((state) => state.defaultApplication);
  const favorites = useWorkspaceBootstrapStore((state) => state.favorites);
  const frequent = useWorkspaceBootstrapStore((state) => state.frequent);
  const recent = useWorkspaceBootstrapStore((state) => state.recent);
  const recommended = useWorkspaceBootstrapStore((state) => state.recommended);
  const recentFixedApps = useWorkspaceBootstrapStore((state) => state.recentFixedApps);
  const ensureApplication = useApplicationEntityStore((state) => state.ensure);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const rememberConversation = useWorkspaceStore((state) => state.rememberConversation);
  const queuePrompt = useWorkspaceStore((state) => state.queuePrompt);
  const [resolving, setResolving] = useState(false);

  useEffect(() => { void loadBootstrap(); }, [loadBootstrap]);

  const paramConversation = searchParams.get('conversation');
  const conversationParam = paramConversation
    && Number.isInteger(Number(paramConversation)) ? paramConversation : null;

  // `/?conversation=N` is the history-sidebar deep link. It carries no
  // application, so resolve the owning application — ONE row, by id
  // (执行报告 §13) — and hand over to the real chat workspace. The home
  // workspace itself never renders history.
  useEffect(() => {
    if (!conversationParam) return undefined;
    // The "no application" branch must only run once the bootstrap has
    // landed, otherwise the ?conversation param is dropped on a race.
    if (bootstrapLoading) return undefined;
    let active = true;
    setResolving(true);
    api.get<any>(`/conversations/${conversationParam}/`)
      .then(async (detail) => {
        if (!active) return;
        const target = await ensureApplication(detail?.application_id);
        if (!active) return;
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
  }, [conversationParam, bootstrapLoading, ensureApplication]);

  const handleRouteSend = async (raw: string): Promise<SendDecision> => {
    const { route, target } = await routeComposerText(
      raw, defaultApplication?.id ?? null);
    switch (route.action) {
      case 'passthrough':
        return { action: 'send', content: raw };
      case 'send':
        return { action: 'send', content: route.content };
      case 'ignore':
        return { action: 'ignore' };
      case 'open': {
        if (!target) return { action: 'send', content: raw };
        navigate(`/app/${target.slug}`);
        return { action: 'handled' };
      }
      case 'switch': {
        if (!target) return { action: 'send', content: raw };
        if (route.content) queuePrompt(target.id, route.content);
        openApplication(target.id);
        navigate(`/chat/${target.slug}`);
        return { action: 'handled' };
      }
      default:
        return { action: 'send', content: raw };
    }
  };

  const openApplicationWorkspace = (application: ApplicationSummary) => {
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
        application={defaultApplication ?? undefined}
        conversationId={null}
        onRouteSend={handleRouteSend}
        title="今天想做什么？"
        description="选择一个智能体，或直接输入你的需求开始。"
        emptyState={(
          isMobile ? (
            // §27: the desktop 多分组 HomeShortcuts is NOT rendered on mobile —
            // it is kept intact for PC, and mobile gets an Agent-first surface.
            <MobileHomeSurface />
          ) : (
            <div className="home-workspace">
              <div className="home-workspace__hero">
                <h2 className="home-workspace__title">今天想做什么？</h2>
                <p className="home-workspace__subtitle">
                  选择一个智能体继续，或直接在下方输入你的需求。
                </p>
              </div>
              <HomeShortcuts
                favorites={favorites}
                frequent={frequent}
                recent={recent}
                recommended={recommended}
                recentFixedApps={recentFixedApps}
                onOpen={openApplicationWorkspace}
                onToggleFavorite={toggleFavorite}
              />
            </div>
          )
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
