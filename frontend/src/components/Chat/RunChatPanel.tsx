/**
 * RunChatPanel — chat surface on the unified Run API (v2).
 *
 * Flow: input -> POST /api/v2/runs (lazy conversation on first send)
 * -> GET /api/v2/runs/{id}/stream (SSE) -> render unified events:
 * content.delta increments, artifact.discovered pins artifact cards,
 * run.completed/failed ends the turn. Attachments upload before the Run
 * via POST /api/v2/applications/{id}/attachments with client-side
 * validation of the provider's official limits.
 */
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Spin, message as antdMessage } from 'antd';
import { PaperClipOutlined, SendOutlined } from '@ant-design/icons';
import ReactMarkdown from 'react-markdown';
import Avatar from 'antd/es/avatar';
import {
  ATTACHMENT_LIMITS,
  uploadAttachment,
  validateAttachment,
} from '@/services/runApi';
import {
  resolveDefaultApplication,
  useApplicationCatalogStore,
} from '@/stores/useApplicationCatalogStore';
import { useRunChatStore } from '@/stores/useRunChatStore';
import type { ChatMessage } from '@/stores/useRunChatStore';
import { useAuthStore } from '@/stores/useAuthStore';
import {
  agentAvatarFallback,
  agentAvatarUrl,
  agentDisplayName,
  formatUserLabel,
  userAvatarFallback,
  userAvatarUrl,
} from '@/lib/chatIdentity';
import type { V2Application } from '@/services/runApi';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import ArtifactCard from './ArtifactCard';
import './chatSurface.css';
import './RunChatPanel.css';

const API_BASE = import.meta.env.VITE_API_BASE_URL || '/api';

/**
 * Stable chat image. A provider artifact URL goes through the /open 302
 * resolver (24h URL, then CDN), which is slow on first load and must not be
 * re-fetched on every store update — the parent re-renders the whole message
 * on each content.delta. Keying by src and keeping load state here means a
 * re-render only recomputes the frame, never restarts the download; the
 * placeholder avoids the browser's broken-image icon while the 302 resolves.
 */
const ChatImage: React.FC<{
  src: string;
  alt?: string;
}> = ({ src, alt }) => {
  const [loaded, setLoaded] = useState(false);
  const [failed, setFailed] = useState(false);
  if (failed) {
    return <span className="chat-img chat-img--broken" title={alt}>{alt || '图片加载失败'}</span>;
  }
  return (
    <span className="chat-img">
      {!loaded && <span className="chat-img__loading"><Spin size="small" /></span>}
      <img
        src={src}
        alt={alt || ''}
        loading="lazy"
        onLoad={() => setLoaded(true)}
        onError={() => setFailed(true)}
      />
    </span>
  );
};

/**
 * Render assistant markdown with provider-relative artifact refs rewritten
 * to our 302 resolver. Aily embeds generated files as
 * `artifacts/<artifactName>/<...>/<filename>` (sandbox-relative); the
 * browser resolves that against the SPA origin and 404s. The first path
 * segment after `artifacts/` is the provider artifact name, so we match it
 * against the run's artifacts (name or filename) and rewrite to /open,
 * which resolves a fresh 24h provider URL.
 *
 * The delta carrying the markdown arrives BEFORE artifact.discovered, so an
 * as-yet-unmatched ref must render a placeholder instead of a broken <img>
 * that 404s against the SPA origin and then "repairs" when the artifact
 * lands — that repair cycle is the 裂开再展示/闪烁 symptom.
 */
const MarkdownWithArtifacts: React.FC<{
  content: string;
  artifacts?: { artifactId: string; name: string }[];
}> = ({ content, artifacts }) => {
  /** Matched /open URL, or null when the ref is still unresolved. */
  const resolveArtifactSrc = useCallback((src: string): string | null => {
    const path = src.replace(/^\.?\/?/, '').split(/[?#]/)[0];
    const match = path.match(/^artifacts?\/([^/]+)(?:\/.*)?$/i);
    if (match) {
      const refName = decodeURIComponent(match[1]);
      const hit = (artifacts || []).find((a) => {
        const names = [a.name, (a.name || '').split(/[\\/]/).pop() || ''];
        return names.includes(refName) || (a.name || '').startsWith(refName);
      });
      if (hit) return `${API_BASE}/v2/artifacts/${hit.artifactId}/open`;
    }
    // Also tolerate a bare filename that matches an artifact name exactly.
    const bare = (artifacts || []).find(
      (a) => a.name && a.name === path.split('/').pop());
    if (bare) return `${API_BASE}/v2/artifacts/${bare.artifactId}/open`;
    return null;
  }, [artifacts]);

  // Stable component identities: ReactMarkdown's `components` prop is a
  // render-time lookup table, and an inline object would give every <img> a
  // NEW component type on each parent render — unmounting and remounting the
  // ChatImage subtree, restarting the 302 → CDN download on every content
  // delta. Memoized on artifacts only, so streaming text updates never
  // change it.
  const components = useMemo(() => ({
    img: ({ src, alt }: { src?: string; alt?: string }) => {
      if (typeof src !== 'string') {
        return <img src={src} alt={alt || ''} loading="lazy" />;
      }
      if (/^(https?:|data:|blob:)/i.test(src)) {
        return <ChatImage src={src} alt={alt} />;
      }
      const resolved = resolveArtifactSrc(src);
      // Unknown artifact ref: placeholder, never a broken <img> that
      // flickers into place once artifact.discovered lands.
      return resolved
        ? <ChatImage src={resolved} alt={alt} />
        : <span className="chat-img chat-img--pending" title={src}>{alt || '生成产物'}</span>;
    },
    a: ({ href, children, ...rest }: any) => (
      <a
        href={typeof href === 'string'
          ? (resolveArtifactSrc(href) ?? href)
          : href}
        target="_blank"
        rel="noreferrer"
        {...rest}
      >
        {children}
      </a>
    ),
  }), [resolveArtifactSrc]);

  return (
    <ReactMarkdown components={components}>
      {content}
    </ReactMarkdown>
  );
};

interface PendingUpload {
  key: string;
  name: string;
  state: 'uploading' | 'ready' | 'failed';
  attachmentId?: string;
}

/**
 * Send interception result. The workspace uses it to route `@application`
 * instructions (§36): the panel stays provider- and routing-agnostic.
 */
export type SendDecision =
  /** Send this content in the application the panel is bound to. */
  | { action: 'send'; content: string }
  /** The caller took over (switched workspace / opened a fixed page). */
  | { action: 'handled' }
  /** Nothing to send; keep the composer text as typed. */
  | { action: 'ignore' };

export interface RunChatPanelProps {
  /** Application providing the runtime binding. Falls back to the first
   *  available chat application when omitted (Home Workspace behaviour). */
  applicationId?: number;
  /** The resolved application, when the caller already holds it. Drives the
   *  agent's display name/avatar in the message list; without it the panel
   *  falls back to the shared catalog (see `effectiveApplication`). */
  application?: V2Application;
  /** Existing conversation to continue; null = lazy create on first send. */
  conversationId?: number | null;
  onConversationCreated?: (conversationId: number) => void;
  title?: string;
  description?: string;
  suggestions?: { icon?: string; label: string; text: string }[];
  /** Prefill the composer once (guided prompt flows). */
  draftText?: string;
  /** Report every composer edit so the workspace can persist the draft. */
  onDraftChange?: (text: string) => void;
  /** Intercept the submitted text before the Run is created. */
  onRouteSend?: (raw: string) => SendDecision;
  /** Send once, then hand control back (used by @mention routing). */
  autoSend?: { id: number; text: string } | null;
  /** Restore the message list scroll offset of this workspace (§29). */
  initialScrollTop?: number;
  onScrollTopChange?: (scrollTop: number) => void;
  /** Replace the default empty state (home workspace shows shortcuts). */
  emptyState?: React.ReactNode;
}

const RunChatPanel: React.FC<RunChatPanelProps> = ({
  applicationId,
  application: applicationProp,
  conversationId,
  onConversationCreated,
  title,
  description,
  suggestions = [],
  draftText,
  onDraftChange,
  onRouteSend,
  autoSend,
  initialScrollTop,
  onScrollTopChange,
  emptyState,
}) => {
  const { user } = useAuthStore();
  const {
    conversations,
    isLoading,
    error,
    loadConversation,
    sendMessage,
    clearError,
  } = useRunChatStore();
  const [inputValue, setInputValue] = useState(draftText || '');
  const [sending, setSending] = useState(false);
  // Shared catalog: the same main-agent resolution the home workspace uses,
  // so the composer and the shortcut list can never disagree (§38).
  const applications = useApplicationCatalogStore((state) => state.applications);
  const loadApplications = useApplicationCatalogStore((state) => state.load);
  const catalogLoading = useApplicationCatalogStore((state) => state.isLoading);
  const [pendingUploads, setPendingUploads] = useState<PendingUpload[]>([]);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const messagesRef = useRef<HTMLDivElement>(null);
  // Scroll restore applies once, and only to a list that already has content
  // (restoring into an empty list would be overwritten by the auto-scroll).
  const pendingScrollRestore = useRef<number | null>(
    initialScrollTop && initialScrollTop > 0 ? initialScrollTop : null);
  // Guided drafts arrive as a prop change; only adopt them once per value.
  const lastDraftRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (draftText && draftText !== lastDraftRef.current) {
      setInputValue(draftText);
    }
    lastDraftRef.current = draftText;
  }, [draftText]);

  const updateInput = (value: string) => {
    setInputValue(value);
    onDraftChange?.(value);
  };

  useEffect(() => {
    // Only the Home Workspace path has no bound application; the shared
    // catalog is loaded (and cached) by the workspace shell.
    if (!applicationId) void loadApplications();
  }, [applicationId, loadApplications]);

  useEffect(() => {
    // History restore: only for a conversation the store does not already
    // hold. Our own sends populate the store immediately; loading here
    // would replace the streaming bubbles with the server's partial state
    // (only the user message exists until reconciliation finishes) and the
    // deltas received in between would be lost forever.
    if (!conversationId) return;
    if (!useRunChatStore.getState().conversations[conversationId]) {
      void loadConversation(conversationId);
    }
  }, [conversationId, loadConversation]);

  // Render from the prop conversation when given (history/deep link), else
  // from the store's active conversation (our own first send created it a
  // render earlier than the URL prop arrives).
  const storeActiveConversationId = useRunChatStore(
    (state) => state.activeConversationId);
  const displayConversationId = conversationId ?? storeActiveConversationId;
  const activeConversation = displayConversationId
    ? conversations[displayConversationId]
    : null;
  const messages = activeConversation?.messages || [];
  const streaming = activeConversation?.activeRunId != null;

  useEffect(() => {
    const el = messagesRef.current;
    if (!el || messages.length === 0) return;
    if (pendingScrollRestore.current != null) {
      el.scrollTop = pendingScrollRestore.current;
      pendingScrollRestore.current = null;
      return;
    }
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  // Fallback application for the Home Workspace composer: the explicit main
  // agent, else the first bound chat application (§38).
  const fallbackApplication = useMemo(
    () => (applicationId ? null : resolveDefaultApplication(applications)),
    [applicationId, applications]);
  // The agent that answers here. Its name and avatar label every assistant
  // message — the chat must never show a generic 助手 (§46 Unified User).
  const effectiveApplication = useMemo(
    () => applicationProp
      || (applicationId
        ? applications.find((app) => app.id === applicationId)
        : undefined)
      || fallbackApplication,
    [applicationProp, applicationId, applications, fallbackApplication]);
  const capabilities = effectiveApplication?.capabilities || null;
  const supportsAttachment = capabilities
    ? Boolean(capabilities.attachment)
    : true;
  const effectiveApplicationId = effectiveApplication?.id;

  const handleFiles = async (files: FileList | null) => {
    if (!files?.length || !effectiveApplicationId) return;
    const next: PendingUpload[] = [];
    for (const file of Array.from(files)) {
      const key = `${file.name}-${Date.now()}-${Math.random()}`;
      const violation = validateAttachment(file);
      if (violation) {
        antdMessage.warning(`${violation.name}：${violation.reason}`);
        continue;
      }
      if (pendingUploads.length + next.length + 1 > ATTACHMENT_LIMITS.maxPerRun) {
        antdMessage.warning(`单次最多 ${ATTACHMENT_LIMITS.maxPerRun} 个附件`);
        break;
      }
      next.push({ key, name: file.name, state: 'uploading' });
      setPendingUploads((current) => [...current, ...next]);
      try {
        const attachment = await uploadAttachment(effectiveApplicationId, file);
        setPendingUploads((current) => current.map((item) => (
          item.key === key
            ? { ...item, state: 'ready', attachmentId: attachment.id }
            : item
        )));
      } catch {
        antdMessage.error(`${file.name} 上传失败`);
        setPendingUploads((current) => current.filter((item) => item.key !== key));
      }
    }
  };

  const removeUpload = (key: string) => {
    setPendingUploads((current) => current.filter((item) => item.key !== key));
  };

  const handleSend = async (rawOverride?: string) => {
    const raw = (rawOverride ?? inputValue).trim();
    if (!raw || sending || streaming) return;

    // `@application` routing is decided by the caller (§36): it may strip the
    // mention, switch workspace, or open a fixed page instead of sending.
    const decision: SendDecision = onRouteSend
      ? onRouteSend(raw)
      : { action: 'send', content: raw };
    if (decision.action === 'ignore') return;
    if (decision.action === 'handled') {
      setInputValue('');
      onDraftChange?.('');
      setPendingUploads([]);
      return;
    }
    const content = decision.content.trim();
    if (!content) return;
    if (!effectiveApplicationId) {
      // Home composer before the catalog arrives: stay quiet instead of
      // warning, and never swallow the text (it stays in the composer).
      if (!catalogLoading) antdMessage.warning('暂无可用的聊天应用');
      return;
    }
    const attachments = pendingUploads
      .filter((u) => u.state === 'ready' && u.attachmentId)
      .map((u) => ({ id: u.attachmentId!, name: u.name }));
    // Clear the composer synchronously BEFORE the async send: the await
    // below lets React re-render with the previous controlled value, which
    // would refill the textarea after our later setInputValue('').
    updateInput('');
    setPendingUploads([]);
    setSending(true);
    try {
      const cid = await sendMessage({
        applicationId: effectiveApplicationId,
        conversationId: conversationId || null,
        content,
        attachments,
      });
      if (cid != null) {
        if (!conversationId) onConversationCreated?.(cid);
      }
    } catch {
      // Store already surfaced the error; restore the draft so the user
      // does not lose their unsent message.
      updateInput(content);
    } finally {
      setSending(false);
    }
  };

  // @mention routing hands a prompt over with the workspace switch (§36);
  // send it exactly once.
  const lastAutoSendRef = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (!autoSend || autoSend.id === lastAutoSendRef.current) return;
    lastAutoSendRef.current = autoSend.id;
    setInputValue(autoSend.text);
    void handleSend(autoSend.text);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoSend]);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      void handleSend();
    }
  };

  const renderMessage = (msg: ChatMessage) => {
    const isUser = msg.role === 'user';
    const isSystem = msg.role === 'system';
    // Both sides carry a face and a real name: the human as 姓名（user_id）,
    // the agent as its application name (§46).
    const senderLabel = isUser
      ? formatUserLabel(user)
      : isSystem ? '系统' : agentDisplayName(effectiveApplication);
    const avatarSrc = isUser
      ? userAvatarUrl(user)
      : isSystem ? '' : agentAvatarUrl(effectiveApplication);
    const avatarText = isUser
      ? userAvatarFallback(user)
      : isSystem ? '系' : agentAvatarFallback(effectiveApplication);
    return (
      <div
        key={msg.id}
        className={`animate-fade-in mb-4 flex gap-3 ${isUser ? 'flex-row-reverse' : 'flex-row'}`}
      >
        <Avatar
          size={36}
          className="run-chat-avatar flex-shrink-0"
          src={avatarSrc || undefined}
          alt={senderLabel}
          style={{
            backgroundColor: isUser ? 'var(--color-primary)' : 'var(--color-bg-elevated)',
            color: isUser ? '#fff' : 'var(--color-primary)',
          }}
        >
          {avatarText}
        </Avatar>
        <div className={`run-chat-bubble ${isUser ? 'run-chat-bubble--user' : ''}`}>
          <div className="run-chat-bubble__header">
            <span className="font-medium chat-sender-name" title={senderLabel}>{senderLabel}</span>
            {msg.status === 'streaming' && <span className="run-chat-dotting">生成中…</span>}
            {msg.status === 'failed' && <span className="run-chat-error-tag">失败</span>}
          </div>
          {isUser ? (
            <span>{msg.content}</span>
          ) : (
            <div className="prose prose-sm dark:prose-invert max-w-none">
              <MarkdownWithArtifacts content={msg.content} artifacts={msg.artifacts} />
            </div>
          )}
          {msg.attachments?.length ? (
            <div className="run-chat-upload-row">
              {msg.attachments.map((a) => (
                <span key={a.id} className="run-chat-upload-chip">
                  <PaperClipOutlined /> {a.name}
                </span>
              ))}
            </div>
          ) : null}
          {msg.error && <div className="run-chat-error-text">{msg.error}</div>}
          {msg.artifacts?.length ? (
            <div className="run-chat-artifacts">
              {msg.artifacts.map((a) => <ArtifactCard key={a.artifactId} artifact={a} />)}
            </div>
          ) : null}
        </div>
      </div>
    );
  };

  const isEmpty = messages.length === 0;
  const readyCount = pendingUploads.filter((u) => u.state === 'ready').length;

  return (
    <div className="chat-container">
      {error && (
        <div className="run-chat-error-banner">
          <Alert message={error} type="error" showIcon closable onClose={clearError} />
        </div>
      )}

      {isEmpty && !isLoading ? (
        // The panel owns the empty-state LAYOUT (centered column, flexible
        // height, composer pinned below); callers only supply the content via
        // `emptyState`. Rendering `emptyState` bare here loses that layout —
        // a max-width block would hug the left edge instead of centering.
        <div className="chat-empty">
          {emptyState ?? (
            <>
              {/* The empty state is this agent's front door: its face, not ✦. */}
              <AgentAvatar
                application={effectiveApplication}
                size={80}
                shape="circle"
                className="chat-empty-icon"
              />
              <h3>{title || '开始你的创作之旅'}</h3>
              <p>{description || '向智能体描述你的需求，AI 会协助你完成专业创作任务'}</p>
              {suggestions.length > 0 && (
                <div className="chat-empty-suggestions">
                  {suggestions.map((s) => (
                    <button
                      key={s.label}
                      className="chat-suggestion"
                      onClick={() => updateInput(s.text)}
                    >
                      {s.icon || '✦'} {s.label}
                    </button>
                  ))}
                </div>
              )}
            </>
          )}
        </div>
      ) : (
        <div
          className="chat-messages"
          ref={messagesRef}
          onScroll={(e) => onScrollTopChange?.(e.currentTarget.scrollTop)}
        >
          <div className="chat-messages-inner">
            {messages.map(renderMessage)}
            <div ref={messagesEndRef} />
          </div>
        </div>
      )}

      <div className="chat-input-area">
        <div className="chat-input-wrapper">
          <div className="run-chat-composer">
            {pendingUploads.length > 0 && (
              <div className="run-chat-upload-list">
                {pendingUploads.map((upload) => (
                  <span key={upload.key} className="run-chat-upload-chip">
                    {upload.state === 'uploading' ? <Spin size="small" /> : <PaperClipOutlined />}
                    <span className="run-chat-upload-name">{upload.name}</span>
                    <button
                      onClick={() => removeUpload(upload.key)}
                      aria-label={`移除 ${upload.name}`}
                    >×</button>
                  </span>
                ))}
              </div>
            )}
            <div className="run-chat-input-row">
              <input
                ref={fileInputRef}
                type="file"
                multiple
                hidden
                accept=".png,.jpg,.jpeg,.pdf"
                onChange={(e) => {
                  void handleFiles(e.target.files);
                  e.target.value = '';
                }}
              />
              <button
                className="run-chat-clip"
                disabled={!supportsAttachment || sending || streaming}
                title={supportsAttachment
                  ? `添加附件（png/jpg/pdf，单轮最多 ${ATTACHMENT_LIMITS.maxPerRun} 个）`
                  : '当前应用不支持附件'}
                onClick={() => fileInputRef.current?.click()}
              >
                <PaperClipOutlined />
              </button>
              <textarea
                value={inputValue}
                onChange={(e) => updateInput(e.target.value)}
                onKeyDown={handleKeyDown}
                placeholder={streaming ? '回复生成中…' : '描述你的需求…（Enter 发送，Shift+Enter 换行；@智能体 可切换）'}
                rows={1}
                disabled={sending || streaming}
                className="run-chat-textarea"
              />
              <button
                className="run-chat-send"
                disabled={!inputValue.trim() || sending || streaming
                  || (!effectiveApplicationId && catalogLoading)}
                onClick={() => void handleSend()}
              >
                <SendOutlined />
              </button>
            </div>
            <div className="run-chat-hint">
              {streaming
                ? '回复生成中…'
                : readyCount > 0
                  ? `已选 ${readyCount} 个附件 · Enter 发送`
                  : (!effectiveApplicationId && catalogLoading)
                    ? '正在加载智能体…'
                    : 'Enter 发送 · Shift+Enter 换行'}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
};

export default RunChatPanel;
