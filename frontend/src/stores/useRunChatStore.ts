/**
 * useRunChatStore — chat state driven by the unified Run event protocol.
 *
 * One Conversation = the user's long session; each send = one Run. Event
 * reduction: content.delta appends incrementally, artifact.discovered pins
 * artifact cards onto the streaming message, and run.completed/failed carry
 * the terminal state (final authoritative text arrives from reconciliation
 * on the backend, carried by the terminal event payload).
 *
 * Business facts (messages/runs/artifacts) always come from the backend;
 * this store only mirrors them for rendering.
 */
import { create } from 'zustand';
import axiosInstance from '@/services/axios';
import {
  createRun,
  fetchRunArtifacts,
  getRun,
} from '@/services/runApi';
import { openRunStream } from '@/services/runStream';
import { useConversationStore } from './useConversationStore';
import type {
  RunArtifactRecord,
  RunEventRecord,
  RunRecord,
} from '@/services/runApi';

/**
 * The sidebar history rail (useConversationStore) fetches once on mount and
 * the shell keeps it mounted forever, so conversations created later in the
 * session (e.g. after switching to another agent) never appeared until a
 * reload. Refresh it — debounced — when a conversation is created and when a
 * run converges (title/preview/message_count change).
 */
let sidebarRefreshTimer: ReturnType<typeof setTimeout> | null = null;
function refreshSidebarDebounced() {
  if (sidebarRefreshTimer != null) return;
  sidebarRefreshTimer = setTimeout(() => {
    sidebarRefreshTimer = null;
    void useConversationStore.getState().fetchConversations();
  }, 1500);
}

export interface ChatArtifact {
  artifactId: string;
  name: string;
  normalizedType: string;
}

export interface ChatMessage {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  created_at: string;
  runId?: string;
  status?: 'streaming' | 'done' | 'failed';
  error?: string;
  /** Non-terminal retry hint (run.retrying keeps the stream alive). */
  retryNotice?: string;
  attachments?: { id: string; name: string }[];
  artifacts?: ChatArtifact[];
}

interface ConversationChat {
  id: number;
  title: string;
  messages: ChatMessage[];
  /** Latest run id per conversation, used to block double sends. */
  activeRunId: string | null;
}

export interface RunChatState {
  conversations: Record<number, ConversationChat>;
  /** Conversation currently rendered by the active workspace. */
  activeConversationId: number | null;
  isLoading: boolean;
  error: string | null;
  lastConversationId: number | null;

  loadConversation: (id: number) => Promise<void>;
  setActiveConversation: (id: number | null) => void;
  sendMessage: (params: {
    applicationId: number;
    conversationId?: number | null;
    content: string;
    attachments?: { id: string; name: string }[];
  }) => Promise<number | null>;
  clearError: () => void;
}

/**
 * Events that reveal the run reached a terminal state. run.retrying is
 * deliberately excluded: a requeued attempt keeps streaming (the run
 * stays active; Execution Correctness Closure). run.interrupted is
 * legacy-only (historical replay) — the retry path now emits
 * run.retrying, terminal failures emit run.failed.
 */
const TERMINAL = new Set(['run.completed', 'run.failed', 'run.cancelled']);

function emptyConversation(id: number, title = ''): ConversationChat {
  return { id, title, messages: [], activeRunId: null };
}

export const useRunChatStore = create<RunChatState>()((set, get) => ({
  conversations: {},
  activeConversationId: null,
  isLoading: false,
  error: null,
  lastConversationId: null,

  loadConversation: async (id: number) => {
    // Never overwrite a conversation with a live run: the server has only
    // the user message until reconciliation finishes, and any deltas we
    // receive during this fetch would be lost (they are not replayed).
    const current = get().conversations[id];
    if (current?.activeRunId) return;
    set({ isLoading: true, error: null });
    try {
      const detail = await axiosInstance.get(`/conversations/${id}/`) as any;
      const existing = current;
      // History messages come from the backend source of truth. Messages
      // created by our own sends already exist server-side too, so a
      // reload after stream end replaces the optimistic copies cleanly.
      const messages: ChatMessage[] = (detail.messages || []).map((m: any) => ({
        id: String(m.id),
        role: m.role,
        content: m.content || '',
        created_at: m.created_at,
        runId: m.metadata?.run_id,
        status: 'done' as const,
        artifacts: (m.metadata?.artifacts || []).map((a: any) => ({
          artifactId: a.artifact_id,
          name: a.name,
          normalizedType: a.normalized_type,
        })),
      }));
      set({
        conversations: {
          ...get().conversations,
          [id]: {
            id,
            title: detail.title || existing?.title || '',
            messages,
            activeRunId: null,
          },
        },
        isLoading: false,
      });
    } catch (error: any) {
      set({
        error: error?.response?.data?.detail || '获取对话详情失败',
        isLoading: false,
      });
    }
  },

  setActiveConversation: (id: number | null) => {
    set({ activeConversationId: id });
  },

  clearError: () => set({ error: null }),

  sendMessage: async ({ applicationId, conversationId, content, attachments }) => {
    set({ error: null });
    let run: RunRecord;
    try {
      run = await createRun(applicationId, content, {
        conversationId: conversationId || null,
        attachmentIds: attachments?.map((a) => a.id) || [],
      });
    } catch (error: any) {
      const detail = error?.response?.data
        ? (typeof error.response.data === 'string' ? error.response.data
          : Object.values(error.response.data)[0])
        : null;
      set({ error: detail || '发送失败，请稍后重试' });
      return null;
    }

    const cid = run.conversation;
    const userMsg: ChatMessage = {
      id: `user-${run.id}`,
      role: 'user',
      content,
      created_at: new Date().toISOString(),
      runId: run.id,
      attachments: attachments?.length ? attachments : undefined,
    };
    const assistantMsg: ChatMessage = {
      id: `run-${run.id}`,
      role: 'assistant',
      content: '',
      created_at: new Date().toISOString(),
      runId: run.id,
      status: 'streaming',
      artifacts: [],
    };

    set((state) => ({
      conversations: {
        ...state.conversations,
        [cid]: {
          ...(state.conversations[cid] || emptyConversation(cid)),
          messages: [
            ...(state.conversations[cid]?.messages || []),
            userMsg,
            assistantMsg,
          ],
          activeRunId: run.id,
        },
      },
      activeConversationId: cid,
      lastConversationId: cid,
    }));

    refreshSidebarDebounced();
    consumeRunEvents(run.id);
    return cid;
  },
}));

/** Reduce one unified event into the streaming assistant message. */
export function applyEvent(state: RunChatState, event: RunEventRecord): Partial<RunChatState> {
  const runId = event.run_id || '';
  // Locate the conversation holding this run's streaming message.
  const conversations = { ...state.conversations };
  let hostCid: number | null = null;
  let idx = -1;
  for (const cid of Object.keys(conversations).map(Number)) {
    const conv = conversations[cid];
    idx = conv.messages.findIndex((m) => m.id === `run-${runId}`);
    if (idx !== -1) { hostCid = cid; break; }
  }
  if (hostCid === null || idx === -1) {
    // The streaming bubble was wiped (e.g. a history reload raced our own
    // send). Re-seed it and fall through so THIS event still applies.
    const cid = state.activeConversationId ?? state.lastConversationId;
    if (cid == null || event.event_type === 'run.started') return {};
    const conv = conversations[cid] || emptyConversation(cid);
    conversations[cid] = {
      ...conv,
      messages: [
        ...conv.messages,
        {
          id: `run-${runId}`,
          role: 'assistant' as const,
          content: '',
          created_at: new Date().toISOString(),
          runId,
          status: 'streaming' as const,
          artifacts: [],
        },
      ],
      activeRunId: runId,
    };
    hostCid = cid;
    idx = conversations[cid].messages.length - 1;
  }
  const conv = conversations[hostCid];
  const message = { ...conv.messages[idx] };
  switch (event.event_type) {
      case 'content.delta':
        message.content += event.payload?.text || '';
        break;
      case 'content.chunk':
        // Persisted coalesced chunk: carries a cumulative `snapshot` when
        // available (replace = self-healing, no dup on replay), else the
        // incremental text (append).
        if (typeof event.payload?.snapshot === 'string') {
          message.content = event.payload.snapshot;
        } else {
          message.content += event.payload?.text || '';
        }
        break;
      case 'artifact.discovered': {
        const artifacts = [...(message.artifacts || [])];
        const artifactId = event.payload?.artifact_id
          || event.payload?.external_artifact_id || '';
        if (artifactId && !artifacts.some((a) => a.artifactId === artifactId)) {
          artifacts.push({
            artifactId,
            name: event.payload?.name || '生成产物',
            normalizedType: event.payload?.provider_artifact_type || 'file',
          });
        }
        message.artifacts = artifacts;
        break;
      }
      case 'run.completed':
        // Reconciliation text is authoritative when present.
        if (event.payload?.text) message.content = event.payload.text;
        message.status = 'done';
        break;
      case 'run.failed':
        message.status = 'failed';
        message.error = event.payload?.error_message
          || event.payload?.error_code || '执行失败';
        break;
      case 'run.retrying':
        // Non-terminal: the run was requeued for another attempt. Keep
        // streaming — activeRunId stays, status stays 'streaming'.
        if (message.status === 'streaming') {
          const attempt = Number(event.payload?.attempt ?? 0);
          const reason = event.payload?.reason || '';
          message.retryNotice = attempt > 0
            ? `执行中断（${reason}），正在进行第 ${attempt + 1} 次尝试…`
            : '执行中断，正在重试…';
        }
        break;
      case 'run.interrupted':
        // Legacy historical event (pre-closure data): displayed as a
        // failure only when it actually terminated the run.
        message.status = 'failed';
        message.error = '执行被中断';
        break;
      default:
        break;
    }
    const messages = [...conv.messages];
    messages[idx] = message;
    const stillStreaming = message.status === 'streaming';
    conversations[hostCid] = {
      ...conv,
      messages,
      activeRunId: stillStreaming ? conv.activeRunId : null,
    };
    return { conversations };
  }

function consumeRunEvents(runId: string) {
  const stream = openRunStream(runId, {
    onEvent: (event) => {
      const state = useRunChatStore.getState();
      const patch = applyEvent(state, event);
      if (Object.keys(patch).length) useRunChatStore.setState(patch);
      if (TERMINAL.has(event.event_type)) {
        void finalizeRun(runId, event.payload?.text as string | undefined);
      }
    },
    onError: () => {
      // Transport hiccup; the client auto-reconnects with full replay.
    },
  });
  // Track for cleanup elsewhere if the workspace unmounts mid-run.
  activeStreams.set(runId, stream);
}

const activeStreams = new Map<string, { close: () => void }>();

export function closeRunStream(runId: string) {
  activeStreams.get(runId)?.close();
  activeStreams.delete(runId);
}

async function finalizeRun(runId: string, eventText?: string) {
  closeRunStream(runId);
  let run: RunRecord;
  try {
    run = await getRun(runId);
  } catch {
    return;
  }
  const state = useRunChatStore.getState();
  const cid = run.conversation;
  const conv = state.conversations[cid];
  if (!conv) return;

  const messages = conv.messages.map((m) => {
    if (m.id !== `run-${runId}`) return m;
    const failed = run.status === 'failed' || run.status === 'interrupted';
    return {
      ...m,
      content: (eventText ?? run.output?.text) || m.content,
      status: failed ? 'failed' as const : 'done' as const,
      error: failed
        ? (run.error_message || run.error_code || '执行失败')
        : undefined,
    };
  });

  // Attach artifact cards from the run's persisted artifacts (source of
  // truth; covers artifacts discovered before the stream was opened).
  let artifacts: ChatArtifact[] = [];
  try {
    const records: RunArtifactRecord[] = await fetchRunArtifacts(runId);
    artifacts = records.map((r) => ({
      artifactId: r.id,
      name: r.name || '生成产物',
      normalizedType: r.normalized_type,
    }));
  } catch {
    // Artifact listing is best-effort; cards from events still render.
  }

  useRunChatStore.setState({
    conversations: {
      ...state.conversations,
      [cid]: {
        ...conv,
        messages: messages.map((m) => (
          m.id === `run-${runId}` ? { ...m, artifacts } : m
        )),
        activeRunId: null,
      },
    },
  });
  refreshSidebarDebounced();
}
