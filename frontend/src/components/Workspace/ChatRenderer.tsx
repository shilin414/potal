/**
 * ChatRenderer — the chat workspace of one Application (§18).
 *
 * Owns the workspace chrome the shell does NOT own: which conversation this
 * application restores, its draft and scroll offset (§29), and the
 * `@application` routing of the composer (§36).
 *
 * Chat data itself stays on the unified Run API; this component never talks
 * to a provider.
 */
import React, {
  useCallback, useEffect, useLayoutEffect, useRef, useState,
} from 'react';
import { Button, Tooltip } from 'antd';
import { PlusOutlined, StarFilled, StarOutlined } from '@ant-design/icons';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { RunChatPanel } from '@/components/Chat';
import type { SendDecision } from '@/components/Chat/RunChatPanel';
import { useRunChatStore } from '@/stores/useRunChatStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useWorkspaceStore, workspaceStateOf } from '@/stores/useWorkspaceStore';
import { routeComposerText } from '@/lib/composerRouting';
import type { V2Application } from '@/services/runApi';
import ApplicationSwitcher from './ApplicationSwitcher';

interface Props {
  application: V2Application;
}

const ChatRenderer: React.FC<Props> = ({ application }) => {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const toggleFavorite = useWorkspaceBootstrapStore((state) => state.toggleFavorite);
  const workspace = useWorkspaceStore(
    (state) => workspaceStateOf(state.workspaces, application.id));
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const rememberConversation = useWorkspaceStore((state) => state.rememberConversation);
  const setDraft = useWorkspaceStore((state) => state.setDraft);
  const setScrollTop = useWorkspaceStore((state) => state.setScrollTop);
  const queuePrompt = useWorkspaceStore((state) => state.queuePrompt);
  const takePrompt = useWorkspaceStore((state) => state.takePrompt);
  const startNewConversation = useWorkspaceStore((state) => state.startNewConversation);
  const setActiveConversation = useRunChatStore((state) => state.setActiveConversation);

  const paramConversation = searchParams.get('conversation');
  const urlConversationId = paramConversation
    && Number.isInteger(Number(paramConversation))
    ? Number(paramConversation)
    : null;
  // The remembered conversation is only a fallback for the initial mount;
  // a deep link (`?conversation=`) always wins (§28).
  const [conversationId, setConversationId] = useState<number | null>(
    () => urlConversationId ?? workspace.conversationId);
  const [autoSend, setAutoSend] = useState<{ id: number; text: string } | null>(null);
  const [panelKey, setPanelKey] = useState(0);

  useEffect(() => {
    if (urlConversationId != null) setConversationId(urlConversationId);
  }, [urlConversationId]);

  // A prompt handed over by @mention routing in another workspace (§36).
  useEffect(() => {
    const pending = takePrompt(application.id);
    if (pending) setAutoSend({ id: Date.now(), text: pending });
  }, [application.id, takePrompt]);

  // `@application` routing (执行报告 §14): the candidate list is resolved by
  // the SERVER from the token the user typed, so an agent that is not on any
  // loaded page can still be mentioned without downloading the catalog.
  const handleRouteSend = useCallback(async (raw: string): Promise<SendDecision> => {
    const { route, target } = await routeComposerText(raw, application.id);
    switch (route.action) {
      case 'passthrough':
        return { action: 'send', content: raw };
      case 'send':
        return { action: 'send', content: route.content };
      case 'ignore':
        return { action: 'ignore' };
      case 'open': {
        if (!target) return { action: 'send', content: raw };
        rememberConversation(application.id, conversationId);
        navigate(`/app/${target.slug}`);
        return { action: 'handled' };
      }
      case 'switch': {
        if (!target) return { action: 'send', content: raw };
        // Persist this workspace before leaving so switching back restores
        // the conversation/draft/scroll (§29).
        rememberConversation(application.id, conversationId);
        if (route.content) queuePrompt(target.id, route.content);
        openApplication(target.id);
        navigate(`/chat/${target.slug}`);
        return { action: 'handled' };
      }
      default:
        return { action: 'send', content: raw };
    }
  }, [application.id, conversationId, navigate,
    openApplication, queuePrompt, rememberConversation]);

  // Scroll persistence is throttled: the store is persisted, and a write per
  // scroll event would hammer localStorage.
  const pendingScroll = useRef<number | null>(null);
  const scrollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const flushScroll = useCallback(() => {
    if (pendingScroll.current != null) {
      setScrollTop(application.id, pendingScroll.current);
      pendingScroll.current = null;
    }
  }, [application.id, setScrollTop]);
  const handleScroll = useCallback((top: number) => {
    pendingScroll.current = top;
    if (scrollTimer.current) return;
    scrollTimer.current = setTimeout(() => {
      scrollTimer.current = null;
      flushScroll();
    }, 400);
  }, [flushScroll]);
  useEffect(() => () => {
    if (scrollTimer.current) clearTimeout(scrollTimer.current);
    flushScroll();
  }, [flushScroll]);

  const handleNewConversation = () => {
    startNewConversation(application.id);
  };

  // External "new conversation" (sidebar 新建, mobile drawer) while THIS
  // workspace is already mounted: navigating to the same /chat/:slug URL
  // remounts nothing, so startNewConversation bumps newConversationTick and
  // the tick change performs the same reset as the header button. Layout
  // effect so the reset lands before the first paint (no stale flash).
  const newConversationTick = workspace.newConversationTick || 0;
  const lastTickRef = useRef(newConversationTick);
  useLayoutEffect(() => {
    if (newConversationTick === lastTickRef.current) return;
    lastTickRef.current = newConversationTick;
    setActiveConversation(null);
    setConversationId(null);
    setAutoSend(null);
    pendingScroll.current = 0;
    setPanelKey((key) => key + 1);
    if (urlConversationId != null) setSearchParams({}, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [newConversationTick]);

  return (
    <div className="workspace-host">
      <header className="chat-renderer__head">
        <ApplicationSwitcher />
        <span className="chat-renderer__head-spacer" />
        <Tooltip title={application.is_favorite ? '取消收藏' : '收藏该智能体'}>
          <Button
            type="text"
            aria-label={application.is_favorite ? '取消收藏' : '收藏'}
            icon={application.is_favorite ? <StarFilled /> : <StarOutlined />}
            onClick={() => void toggleFavorite(application.id, application)}
          />
        </Tooltip>
        <Button type="text" icon={<PlusOutlined />} onClick={handleNewConversation}>
          新建对话
        </Button>
      </header>
      <div className="chat-renderer__body">
        <RunChatPanel
          key={panelKey}
          applicationId={application.id}
          application={application}
          conversationId={conversationId}
          draftText={workspace.draft}
          onDraftChange={(text) => setDraft(application.id, text)}
          onRouteSend={handleRouteSend}
          autoSend={autoSend}
          initialScrollTop={workspace.scrollTop}
          onScrollTopChange={handleScroll}
          title={application.name}
          description={application.description}
          onConversationCreated={(id) => {
            setConversationId(id);
            rememberConversation(application.id, id);
            setSearchParams({ conversation: String(id) }, { replace: true });
          }}
        />
      </div>
    </div>
  );
};

export default ChatRenderer;
