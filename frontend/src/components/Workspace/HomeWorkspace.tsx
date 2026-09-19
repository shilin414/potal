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
import { Button, Empty, Spin } from 'antd';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { api } from '@/services/api';
import { RunChatPanel } from '@/components/Chat';
import type { SendDecision } from '@/components/Chat/RunChatPanel';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import type { ApplicationSummary } from '@/services/runApi';
import WorkbenchHome from '@/workbench/home/WorkbenchHome';
import { useRecentCapabilities } from '@/workbench/capability/useRecentCapabilities';
import './HomeWorkspace.css';

const HomeWorkspace: React.FC = () => {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  // Mobile swaps ONLY the empty state (§6.1): the WorkspaceHost, the Run API
  // and this component's deep-link / @mention handling are shared, so the two
  // shells can never drift on what a shortcut does — only on how it looks.
  const bootstrapLoading = useWorkspaceBootstrapStore((state) => state.isLoading);
  const loadBootstrap = useWorkspaceBootstrapStore((state) => state.load);
  const defaultApplication = useWorkspaceBootstrapStore((state) => state.defaultApplication);
  const recentCapabilities = useRecentCapabilities(8);
  const recommended = useWorkspaceBootstrapStore((state) => state.recommended);
  const ensureApplication = useApplicationEntityStore((state) => state.ensure);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const rememberConversation = useWorkspaceStore((state) => state.rememberConversation);
  const [resolving, setResolving] = useState(false);
  /** Distinguishes "no such conversation/application" from "offline" (P1-6). */
  const [resolveFailed, setResolveFailed] = useState(false);

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
    setResolveFailed(false);
    api.get<any>(`/conversations/${conversationParam}/`)
      .then(async (detail) => {
        if (!active) return;
        let target: Awaited<ReturnType<typeof ensureApplication>>;
        try {
          target = await ensureApplication(detail?.application_id);
        } catch {
          // A 500 / timeout / offline backend (二次复审 P1-6). The old code
          // swallowed this together with a 404 and then DELETED the user's
          // `?conversation=` deep link, so a transient network error
          // silently destroyed real navigation state. Keep the URL, show a
          // retry.
          if (active) setResolveFailed(true);
          return;
        }
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
        // The conversation detail itself failed. Also not a 404: keep the
        // deep link and offer a retry.
        if (active) setResolveFailed(true);
      })
      .finally(() => { if (active) setResolving(false); });
    return () => { active = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conversationParam, bootstrapLoading, ensureApplication]);

  const handleRouteSend = async (raw: string): Promise<SendDecision> => ({
    action: 'send',
    content: raw,
  });

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

  // A FAILED deep-link lookup keeps the `?conversation=` param and offers a
  // reload (二次复审 P1-6) — the URL is the user's navigation state, so a
  // transport error must not be "handled" by deleting it.
  if (resolveFailed) {
    return (
      <div className="workspace-host__missing">
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description="任务加载失败，请检查网络后重试"
        >
          <Button type="primary" onClick={() => window.location.reload()}>
            重新加载
          </Button>
        </Empty>
      </div>
    );
  }

  return (
    <div className="workspace-host">
      <RunChatPanel
        applicationId={defaultApplication?.id}
        application={defaultApplication ?? undefined}
        conversationId={null}
        onRouteSend={handleRouteSend}
        title="今天想完成什么？"
        description="选择一个能力，或直接描述你的任务开始。"
        emptyState={(
          <WorkbenchHome
            current={defaultApplication}
            recent={recentCapabilities}
            recommended={recommended}
            onOpen={openApplicationWorkspace}
          />
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
