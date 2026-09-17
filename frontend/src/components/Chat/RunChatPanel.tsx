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
import {
  Alert,
  Button,
  Input,
  Modal,
  Spin,
  Tooltip,
  message as antdMessage,
} from 'antd';
import {
  CheckOutlined,
  CopyOutlined,
  ExportOutlined,
  PaperClipOutlined,
  SendOutlined,
  TeamOutlined,
} from '@ant-design/icons';
import Avatar from 'antd/es/avatar';
import {
  ATTACHMENT_LIMITS,
  uploadAttachment,
  validateAttachment,
} from '@/services/runApi';
import {
  buildShareUrl,
  createConversationShare,
} from '@/services/shareApi';
import {
  resolveDefaultApplication,
  useApplicationCatalogStore,
} from '@/stores/useApplicationCatalogStore';
import { useRunChatStore } from '@/stores/useRunChatStore';
import type { ChatMessage } from '@/stores/useRunChatStore';
import { useAuthStore } from '@/stores/useAuthStore';
import { useWorkspaceStore, workspaceStateOf } from '@/stores/useWorkspaceStore';
import {
  agentAvatarFallback,
  agentAvatarUrl,
  agentDisplayName,
  formatUserLabel,
  userAvatarFallback,
  userAvatarUrl,
} from '@/lib/chatIdentity';
import {
  buildSkillAugmentedContent,
  resolveSelectedSkills,
  skillButtonLabel,
} from '@/lib/agentSkills';
import type { AgentSkill, V2Application } from '@/services/runApi';
import { useIsMobile } from '@/shell/useIsMobile';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import {
  MobileAttachmentSheet,
  MobileComposer,
  MobileSkillSheet,
} from '@/components/Mobile';
import type { PendingUpload } from '@/components/Mobile';
import ArtifactCard from './ArtifactCard';
import { MarkdownWithArtifacts } from './ArtifactMarkdown';
import FeishuForwardModal from './FeishuForwardModal';
import './chatSurface.css';
import './RunChatPanel.css';

/** Stable empty list: `?? []` in render would churn every memo depending on it. */
const NO_SKILLS: AgentSkill[] = [];

/**
 * What the ONE hidden file input accepts. Extensions AND mime types, because
 * some Android/飞书 pickers match on one and some on the other; the real
 * enforcement is `validateAttachment`, which also knows the size limits (§9.3).
 */
const ATTACHMENT_ACCEPT = '.png,.jpg,.jpeg,.pdf,image/png,image/jpeg,application/pdf';

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
  // Mobile-only sheets (§8/§10): the composer's 技能 and ＋ entries.
  const [skillSheetOpen, setSkillSheetOpen] = useState(false);
  const [attachmentSheetOpen, setAttachmentSheetOpen] = useState(false);
  const isMobile = useIsMobile();
  const setSelectedSkillIds = useWorkspaceStore((state) => state.setSelectedSkillIds);
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

  // ── 转发/分享（多选模式）─────────────────────────────────────────────
  // 服务端快照式分享：选中若干消息后创建分享，后端固化消息 ID 并返回随机
  // token；公开页只回显快照内的消息。参考实现把 ?msg=id1,id2 拼在 URL 上、
  // 由前端过滤，去掉后缀即可看到整段对话——这里从服务端杜绝。
  const [selectMode, setSelectMode] = useState(false);
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [creatingShare, setCreatingShare] = useState(false);
  const [shareResult, setShareResult] = useState<
    { url: string; count: number; token: string } | null
  >(null);
  const [copied, setCopied] = useState(false);
  const [forwardOpen, setForwardOpen] = useState(false);

  /** Only persisted messages (numeric server ids) can be shared. */
  const isShareable = useCallback(
    (msg: ChatMessage) => /^\d+$/.test(msg.id) && msg.status !== 'streaming',
    [],
  );

  /**
   * 最后一个「回复」（最后一条助手消息）。
   * 复制/分享只挂在这一条的气泡下方——不再散落在每个气泡的右上角。
   */
  const lastReplyIndex = useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i -= 1) {
      if (messages[i].role === 'assistant') return i;
    }
    return -1;
  }, [messages]);

  // 「复制」的即时反馈：短暂切成「已复制」再回到「复制」。
  const [copiedMsgId, setCopiedMsgId] = useState<string | null>(null);
  const copyTimer = useRef<number | null>(null);

  const handleCopyMessage = async (msg: ChatMessage) => {
    const text = msg.content ?? '';
    if (!text.trim()) return;
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      antdMessage.error('复制失败，请手动选择文本复制');
      return;
    }
    setCopiedMsgId(msg.id);
    if (copyTimer.current) window.clearTimeout(copyTimer.current);
    copyTimer.current = window.setTimeout(() => setCopiedMsgId(null), 1600);
  };

  useEffect(() => () => {
    if (copyTimer.current) window.clearTimeout(copyTimer.current);
  }, []);

  // Switching conversations must reset the selection (no cross-conversation
  // accidental forwarding — same guard the reference implementation added).
  useEffect(() => {
    setSelectMode(false);
    setSelectedIds([]);
  }, [displayConversationId]);

  /**
   * Enter select mode, optionally pre-selecting one message (bubble hover
   * icon). Freshly streamed bubbles still carry temp ids (`run-{runId}`), so
   * reload the conversation first to swap in the server ids — blocked while
   * a run is active (a reload there would drop live deltas).
   */
  const startSelect = async (initialIndex?: number) => {
    if (streaming) {
      antdMessage.warning('回复生成中，请稍后再转发');
      return;
    }
    const cid = displayConversationId;
    if (cid == null) return;
    let list = messages;
    if (list.some((m) => !/^\d+$/.test(m.id))) {
      await loadConversation(cid);
      list = useRunChatStore.getState().conversations[cid]?.messages || list;
    }
    const picked: string[] = [];
    if (initialIndex != null) {
      const target = list[initialIndex];
      if (target && /^\d+$/.test(target.id)) picked.push(target.id);
    }
    setSelectMode(true);
    setSelectedIds(picked);
  };

  const toggleSelect = (msg: ChatMessage) => {
    if (!isShareable(msg)) return;
    setSelectedIds((current) => (
      current.includes(msg.id)
        ? current.filter((id) => id !== msg.id)
        : [...current, msg.id]
    ));
  };

  const exitSelect = () => {
    setSelectMode(false);
    setSelectedIds([]);
  };

  const handleCreateShare = async () => {
    if (displayConversationId == null) return;
    const ids = messages
      .filter((m) => selectedIds.includes(m.id) && /^\d+$/.test(m.id))
      .map((m) => Number(m.id));
    if (!ids.length) return;
    setCreatingShare(true);
    try {
      const share = await createConversationShare(displayConversationId, ids);
      setShareResult({
        url: buildShareUrl(share.share_token),
        count: share.message_count,
        token: share.share_token,
      });
      setCopied(false);
      exitSelect();
    } catch (e: any) {
      antdMessage.error(e?.response?.data?.detail || '创建分享失败，请稍后重试');
    } finally {
      setCreatingShare(false);
    }
  };

  const handleCopyShare = async () => {
    if (!shareResult) return;
    try {
      await navigator.clipboard.writeText(shareResult.url);
      setCopied(true);
      antdMessage.success('链接已复制');
    } catch {
      antdMessage.error('复制失败，请手动复制');
    }
  };

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

  // ── 技能配置 (design report §9) ──────────────────────────────────────
  // Skills belong to the AGENT, so the list comes straight off the resolved
  // application rather than from a standalone skill catalog. The selection
  // lives in the workspace store, keyed by application, so switching agents
  // restores each one's own combination (§9.4).
  const availableSkills = effectiveApplication?.skills ?? NO_SKILLS;
  const storedSkillIds = useWorkspaceStore((state) => (
    workspaceStateOf(state.workspaces, effectiveApplicationId).selectedSkillIds));
  // resolveSelectedSkills drops ids the agent no longer offers AND re-sorts
  // into catalog order, which is what keeps the composed content — and hence
  // the idempotency hash — stable regardless of the order chips were tapped.
  const selectedSkills = useMemo(
    () => resolveSelectedSkills(availableSkills, storedSkillIds),
    [availableSkills, storedSkillIds]);

  const handleSkillChange = (skillIds: string[]) => {
    if (effectiveApplicationId == null) return;
    setSelectedSkillIds(effectiveApplicationId, skillIds);
  };

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
    // 技能 are applied AFTER routing, never before: `routeMention` only
    // recognises a LEADING `@mention`, so prepending the skill prompts first
    // would push `@销售助手 ...` off the front and silently stop routing it
    // (§36). The provider then sees the composed text — the fragment changes
    // `content`, so the idempotency hash already covers the selection.
    const outgoingContent = buildSkillAugmentedContent(selectedSkills, content);
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
        content: outgoingContent,
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

  const renderMessage = (msg: ChatMessage, index: number) => {
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
    const selectable = selectMode && isShareable(msg);
    const checked = selectedIds.includes(msg.id);
    return (
      <div
        key={msg.id}
        className={`run-chat-msg-row ${isUser ? 'run-chat-msg-row--user ' : ''}animate-fade-in mb-4 flex gap-3 ${isUser ? 'flex-row-reverse' : 'flex-row'}`}
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
        <div
          className={`run-chat-msg-body ${selectMode ? 'run-chat-msg-body--select' : ''} ${selectable ? 'run-chat-msg-body--selectable' : ''}`}
          onClick={selectable ? () => toggleSelect(msg) : undefined}
        >
          {selectMode && (
            <span
              className={`run-chat-select-check ${checked ? 'run-chat-select-check--on' : ''} ${!isShareable(msg) ? 'run-chat-select-check--off' : ''}`}
              aria-hidden
            >
              {checked && <CheckOutlined />}
            </span>
          )}
          <div className={`run-chat-bubble ${isUser ? 'run-chat-bubble--user' : ''}`}>
            <div className="run-chat-bubble__header">
              <span className="font-medium chat-sender-name" title={senderLabel}>{senderLabel}</span>
              {msg.status === 'streaming' && !msg.retryNotice && <span className="run-chat-dotting">生成中…</span>}
              {msg.status === 'streaming' && msg.retryNotice && <span className="run-chat-dotting">{msg.retryNotice}</span>}
              {msg.status === 'failed' && <span className="run-chat-error-tag">失败</span>}
              {msg.status === 'cancelled' && (
                <Tooltip title={msg.error || '应用或运行配置已停用，本次执行已取消。'}>
                  <span className="run-chat-cancelled-tag">已取消</span>
                </Tooltip>
              )}
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
          {msg.error && (
            <div
              className={`run-chat-error-text${msg.status === 'cancelled' ? ' run-chat-error-text--cancelled' : ''}`}
            >
              {msg.error}
            </div>
          )}
          {msg.artifacts?.length ? (
            <div className="run-chat-artifacts">
              {msg.artifacts.map((a) => <ArtifactCard key={a.artifactId} artifact={a} />)}
            </div>
          ) : null}
          {/* 只在最后一个回复的下方出现：转发在左、复制在右，都是纯图标按钮。 */}
          {index === lastReplyIndex && !selectMode && msg.status !== 'streaming' && (
            <div className="run-chat-actions">
              <Tooltip title="转发这条回复">
                <button
                  type="button"
                  className="run-chat-action"
                  aria-label="转发这条回复"
                  onClick={(e) => {
                    e.stopPropagation();
                    void startSelect(index);
                  }}
                >
                  <ExportOutlined />
                </button>
              </Tooltip>
              {msg.content?.trim() ? (
                <Tooltip title={copiedMsgId === msg.id ? '已复制' : '复制这条回复'}>
                  <button
                    type="button"
                    className={`run-chat-action${copiedMsgId === msg.id ? ' run-chat-action--copied' : ''}`}
                    aria-label={copiedMsgId === msg.id ? '已复制' : '复制这条回复'}
                    onClick={(e) => {
                      e.stopPropagation();
                      void handleCopyMessage(msg);
                    }}
                  >
                    {copiedMsgId === msg.id ? <CheckOutlined /> : <CopyOutlined />}
                  </button>
                </Tooltip>
              ) : null}
            </div>
          )}
          </div>
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
        <div className={`chat-empty${isMobile ? ' chat-empty--mobile' : ''}`}>
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
            {messages.map((m, i) => renderMessage(m, i))}
            <div ref={messagesEndRef} />
          </div>
        </div>
      )}

      <div className={`chat-input-area${isMobile ? ' chat-input-area--mobile' : ''}`}>
        <div className="chat-input-wrapper">
          {/* ONE hidden picker for both shells: the desktop paperclip and the
              mobile `+` sheet both open THIS input, so validateAttachment →
              uploadAttachment → pendingUploads stays single-sourced. */}
          <input
            ref={fileInputRef}
            type="file"
            multiple
            hidden
            accept={ATTACHMENT_ACCEPT}
            onChange={(e) => {
              void handleFiles(e.target.files);
              e.target.value = '';
            }}
          />
          {selectMode ? (
            <div className="run-chat-select-bar">
              <button className="run-chat-select-btn" onClick={exitSelect}>取消</button>
              <button
                className="run-chat-select-btn"
                onClick={() => {
                  const shareable = messages.filter(isShareable);
                  const allSelected = shareable.length > 0
                    && shareable.every((m) => selectedIds.includes(m.id));
                  setSelectedIds(allSelected ? [] : shareable.map((m) => m.id));
                }}
              >
                全选
              </button>
              <span className="run-chat-select-count">已选 {selectedIds.length} 条消息</span>
              <Button
                type="primary"
                loading={creatingShare}
                disabled={selectedIds.length === 0}
                onClick={() => void handleCreateShare()}
              >
                生成分享链接
              </Button>
            </div>
          ) : isMobile ? (
            <MobileComposer
              value={inputValue}
              sending={sending || streaming}
              uploads={pendingUploads}
              supportsAttachment={supportsAttachment}
              skillLabel={skillButtonLabel(selectedSkills)}
              hasSkills={availableSkills.length > 0}
              onChange={updateInput}
              onSend={() => void handleSend()}
              onOpenAttachments={() => setAttachmentSheetOpen(true)}
              onOpenSkills={() => setSkillSheetOpen(true)}
              onRemoveUpload={removeUpload}
            />
          ) : (
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
          )}
        </div>
      </div>

      <Modal
        open={!!shareResult}
        onCancel={() => setShareResult(null)}
        footer={null}
        title="分享对话"
        width={520}
        centered
      >
        {shareResult && (
          <div className="run-chat-share-modal">
            <p className="run-chat-share-lead">
              已创建只读快照，仅包含所选 <b>{shareResult.count}</b> 条消息。
              接收者打开链接即可查看，无需登录。
            </p>
            <div className="run-chat-share-link">
              <Input
                readOnly
                value={shareResult.url}
                onFocus={(e) => e.currentTarget.select()}
              />
              <Button
                type="primary"
                icon={<CopyOutlined />}
                onClick={() => void handleCopyShare()}
              >
                {copied ? '已复制' : '复制链接'}
              </Button>
            </div>
            <Button
              block
              icon={<TeamOutlined />}
              onClick={() => setForwardOpen(true)}
            >
              转发到飞书（用户 / 群聊）
            </Button>
            <p className="run-chat-share-note">
              链接指向服务端快照：只包含转发时选中的消息，之后的对话内容不会出现。
            </p>
          </div>
        )}
      </Modal>

      <FeishuForwardModal
        open={forwardOpen}
        shareToken={shareResult?.token ?? null}
        onClose={() => setForwardOpen(false)}
      />

      {/* Mobile-only sheets. Rendered unconditionally (their `open` flag gates
          them) so the composer's buttons never have to mount/unmount trees. */}
      {isMobile && (
        <>
          <MobileSkillSheet
            open={skillSheetOpen}
            skills={availableSkills}
            selectedIds={selectedSkills.map((skill) => skill.id)}
            onChange={handleSkillChange}
            onClose={() => setSkillSheetOpen(false)}
          />
          <MobileAttachmentSheet
            open={attachmentSheetOpen}
            onPickFiles={() => fileInputRef.current?.click()}
            onClose={() => setAttachmentSheetOpen(false)}
          />
        </>
      )}
    </div>
  );
};

export default RunChatPanel;
