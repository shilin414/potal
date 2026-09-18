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
import {
  captureSessionGeneration,
  registerSessionReset,
  sessionStillCurrent,
} from '@/stores/resetSessionState';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import axiosInstance from '@/services/axios';
import {
  createRun,
  fetchRunArtifacts,
  getRun,
  newClientRequestId,
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
function refreshSidebarDebounced(generation = captureSessionGeneration()) {
  if (sidebarRefreshTimer != null) return;
  sidebarRefreshTimer = setTimeout(() => {
    sidebarRefreshTimer = null;
    if (!sessionStillCurrent(generation)) return;
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
  /**
   * 'cancelled' 是管理员撤销（Hard Kill）的终态：它不是 Provider 故障，
   * 因此不能折叠进 'failed'，更不能当成 'done'（第四轮 P1-2）。
   */
  status?: 'streaming' | 'done' | 'failed' | 'cancelled';
  error?: string;
  /** Non-terminal retry hint (run.retrying keeps the stream alive). */
  retryNotice?: string;
  attachments?: { id: string; name: string }[];
  artifacts?: ChatArtifact[];
  /**
   * How many UTF-8 BYTES of `content` this bubble has already rendered.
   *
   * It exists because a durable content.chunk carries an INCREMENTAL slice
   * plus its END offset, and the same text also arrives as transient
   * content.delta frames: appending both duplicates the answer. Comparing
   * against a byte counter (not `content.length`, which counts UTF-16 code
   * units and therefore disagrees with the backend's byte offsets as soon as
   * a Chinese character or an emoji appears) is what lets the reducer append
   * only what is missing. Absent (history messages, re-seeded bubbles) means
   * "derive it from content".
   */
  streamBytes?: number;
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
    /**
     * Retry of a previous send with the SAME id (第九轮 P0-1). Omit it for a
     * new send action; the store keeps the id of the last failed attempt and
     * reuses it automatically when the retried payload is identical, so a
     * manual retry after a lost response replays instead of duplicating.
     */
    clientRequestId?: string;
  }) => Promise<number | null>;
  clearError: () => void;
  /**
   * Forget every transcript (二次复审 P0-2). Chat history is the most
   * sensitive thing this app holds in memory, and it is per-user: it must
   * not survive a logout / user switch.
   */
  clearAll: () => void;
}

/**
 * Events that reveal the run reached a terminal state. run.retrying is
 * deliberately excluded: a requeued attempt keeps streaming (the run
 * stays active; Execution Correctness Closure). run.interrupted is
 * legacy-only (historical replay) — the retry path now emits
 * run.retrying, terminal failures emit run.failed.
 */
const TERMINAL = new Set(['run.completed', 'run.failed', 'run.cancelled']);

/** Hard Kill 的用户文案：管理员撤销不是 Provider 故障，不能写成“执行失败”。 */
const CANCELLED_NOTICE = '应用或运行配置已停用，本次执行已取消。';

/**
 * UTF-8 byte length of a JS string.
 *
 * `string.length` counts UTF-16 code units, so "你" is 1 there and 3 in the
 * backend's byte offsets — using it would mis-align every chunk that follows
 * a non-ASCII character. Iterating with for..of walks CODE POINTS, so an
 * emoji (a surrogate pair) counts as its real 4 bytes instead of 2.
 */
export function utf8ByteLength(text: string): number {
  let bytes = 0;

  for (const ch of text) {
    const cp = ch.codePointAt(0) as number;
    bytes += cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4;
  }
  return bytes;
}

/**
 * The tail of `text` starting at byte offset `skip`, never splitting a
 * character: a byte position that lands inside a multi-byte character skips
 * that character whole, so the rendered string can never contain a broken
 * sequence (which would show up as U+FFFD).
 */
export function utf8SliceFromBytes(text: string, skip: number): string {
  if (skip <= 0) return text;
  let out = '';
  let pos = 0;

  for (const ch of text) {
    if (pos >= skip) out += ch;
    const cp = ch.codePointAt(0) as number;
    pos += cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4;
  }
  return out;
}

/** Bytes of `content` already rendered (see ChatMessage.streamBytes). */
function renderedBytes(message: ChatMessage): number {
  return message.streamBytes ?? utf8ByteLength(message.content);
}

function cancelledNotice(errorCode?: string): string {
  return errorCode === 'execution_disabled' ? CANCELLED_NOTICE : '执行已取消';
}

function emptyConversation(id: number, title = ''): ConversationChat {
  return { id, title, messages: [], activeRunId: null };
}

/**
 * The idempotency identity of the LAST send that failed (第九轮 P0-1).
 *
 * It exists so a manual retry is a replay rather than a second turn. The
 * hazard it closes is invisible from the UI's side: when POST /v2/runs
 * commits but the response is lost (timeout, proxy reset), the user sees
 * "发送失败" and clicks send again — with a fresh id that creates a second
 * run, a second user message and a second provider chat for one message.
 *
 * The identity is released as soon as the send SUCCEEDS: from that point the
 * action is over, so the next click is a new action and gets a new id. Two
 * rapid clicks of the same text therefore produce two identities, which the
 * backend resolves properly (the second is refused with 409
 * "previous turn is still running" while the first is live) — deliberately
 * NOT by silently swallowing the second click, which would be
 * indistinguishable from dropping a real message.
 *
 * A caller that owns its own retry loop can pass `clientRequestId` explicitly
 * and keep the identity itself.
 *
 * Kept in a module-level slot rather than in the store so it survives a
 * component remount and never leaks into persisted state.
 */
let pendingSend: { fingerprint: string; clientRequestId: string } | null = null;

/** Identifies a send ACTION: same fields ⇒ the same user intent. */
function sendFingerprint(
  applicationId: number,
  conversationId: number | null,
  content: string,
  attachmentIds: string[],
): string {
  // Attachment ORDER is not part of the intent, so it is normalized away —
  // the backend hashes the same set the same way.
  return JSON.stringify([
    applicationId,
    conversationId || 0,
    content,
    [...attachmentIds].sort(),
  ]);
}

export const useRunChatStore = create<RunChatState>()((set, get) => ({
  conversations: {},
  activeConversationId: null,
  isLoading: false,
  error: null,
  lastConversationId: null,

  loadConversation: async (id: number) => {
    const generation = captureSessionGeneration();
    // Never overwrite a conversation with a live run: the server has only
    // the user message until reconciliation finishes, and any deltas we
    // receive during this fetch would be lost (they are not replayed).
    const current = get().conversations[id];
    if (current?.activeRunId) return;
    set({ isLoading: true, error: null });
    try {
      const detail = await axiosInstance.get(`/conversations/${id}/`) as any;
      if (!sessionStillCurrent(generation)) return;
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
      if (!sessionStillCurrent(generation)) return;
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

  clearAll: () => {
    // The pending-send identity belongs to the PREVIOUS user's last failed
    // action (第九轮 P0-1): reusing it after a switch would attach B's
    // message to A's idempotency key.
    pendingSend = null;
    // A timer that fires AFTER the switch would refetch A's sidebar (and
    // its callback is not covered by a store resetter).
    if (sidebarRefreshTimer != null) {
      clearTimeout(sidebarRefreshTimer);
      sidebarRefreshTimer = null;
    }
    // Close every live Run stream and drop the registry in one synchronous
    // step (四次复审 P0-R4). `close()` aborts the fetch; the epoch guard in
    // consumeRunEvents additionally drops any callback already queued.
    for (const stream of activeStreams.values()) {
      try {
        stream.close();
      } catch {
        // Best-effort: one dead stream must not leave the rest registered.
      }
    }
    activeStreams.clear();
    set({
      conversations: {},
      activeConversationId: null,
      isLoading: false,
      error: null,
      lastConversationId: null,
    });
  },

  sendMessage: async ({
    applicationId, conversationId, content, attachments, clientRequestId,
  }) => {
    const generation = captureSessionGeneration();
    set({ error: null });
    const attachmentIds = attachments?.map((a) => a.id) || [];
    const fingerprint = sendFingerprint(applicationId, conversationId || null, content, attachmentIds);
    // Same action → same id. Only a request the user actually CHANGED gets a
    // new identity, which is exactly the boundary the backend enforces.
    const requestId = clientRequestId
      || (pendingSend && pendingSend.fingerprint === fingerprint
        ? pendingSend.clientRequestId
        : newClientRequestId());
    let run: RunRecord;
    try {
      run = await createRun(applicationId, content, {
        conversationId: conversationId || null,
        attachmentIds,
        clientRequestId: requestId,
      });
      if (!sessionStillCurrent(generation)) return null;
      pendingSend = null;
      // A new run moves `usage_count` / `last_used_at`, which is exactly what
      // 常用 / 最近使用 / 推荐 are ranked by (二次复审 P1-4). Mark the
      // bootstrap stale instead of refetching it per message: the next home
      // visit re-reads it once.
      useWorkspaceBootstrapStore.getState().invalidate();
    } catch (error: any) {
      if (!sessionStillCurrent(generation)) return null;
      // Remember the identity so the retry replays instead of duplicating.
      pendingSend = { fingerprint, clientRequestId: requestId };
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

    set((state) => {
      const conv = state.conversations[cid] || emptyConversation(cid);
      // An IDEMPOTENT REPLAY returns a run whose messages this store may
      // already hold (the earlier attempt did commit; only its response was
      // lost). Inserting them again would show the same turn twice, so the
      // optimistic insert is keyed on the run id.
      const alreadyPresent = conv.messages.some((m) => m.id === userMsg.id);
      return {
        conversations: {
          ...state.conversations,
          [cid]: {
            ...conv,
            messages: alreadyPresent
              ? conv.messages
              : [...conv.messages, userMsg, assistantMsg],
            activeRunId: run.id,
          },
        },
        activeConversationId: cid,
        lastConversationId: cid,
      };
    });

    refreshSidebarDebounced(generation);
    consumeRunEvents(run.id, generation);
    return cid;
  },
}));

/**
 * Fold one INCREMENTAL text segment into the bubble and return the new
 * rendered-byte count. 第九轮补丁 3.2-A: BOTH transient content.delta and
 * durable content.chunk go through this ONE byte-range reconciliation — the
 * backend now stamps an absolute UTF-8 end offset on both.
 *
 * The same answer reaches the client twice: once as transient content.delta
 * frames (live, never persisted) and once as durable content.chunk events
 * (persisted, replayed after a reconnect). Appending both — what the reducer
 * did before this fix — doubles every answer ("你好" becomes "你好你好").
 *
 * The durable cursor cannot dedupe this either: a transient frame has
 * sequence 0 by definition and never advances it, so a client that saw the
 * deltas and then replays the chunks is outside the cursor's reach entirely.
 * Worse, the SSE gateway deliberately drains buffered live deltas AFTER
 * replaying the durable events (subscribe → replay → drain), so the wire
 * order is genuinely "chunk ABC, then delta A, delta B, delta C" — a plain
 * append produces ABCABC. The only reliable signal is the shared byte
 * offset, which makes the arrival order irrelevant:
 *
 *   end <= rendered            the whole segment was already shown → drop it
 *   start <= rendered < end    partially shown → append only the missing tail
 *   rendered < start           a real gap → keep the bytes we do have
 *
 * Offsets are UTF-8 BYTES, so every comparison goes through utf8ByteLength /
 * utf8SliceFromBytes rather than string.length.
 */
function applyIncrementalRange(
  message: ChatMessage,
  payload: Record<string, any>,
): number {
  const text = typeof payload.text === 'string' ? payload.text : '';
  if (!text) return renderedBytes(message);
  const rendered = renderedBytes(message);
  const end = Number(payload.offset);
  if (!Number.isFinite(end) || end < 0) {
    // Pre-第九轮 incremental chunk without an offset: nothing to compare
    // against, so append (the old behaviour).
    message.content += text;
    return rendered + utf8ByteLength(text);
  }
  const start = end - utf8ByteLength(text);
  if (end <= rendered) {
    // Already rendered via the transient path (or replayed after a
    // reconnect that kept the rendered text).
    return rendered;
  }
  if (start <= rendered) {
    const tail = utf8SliceFromBytes(text, rendered - start);
    message.content += tail;
    return rendered + utf8ByteLength(tail);
  }
  // A gap: bytes between `rendered` and `start` never arrived (e.g. the
  // client attached after the answer had begun). Dropping this segment would
  // lose real text, so append it and jump the counter to its end — the gap
  // is closed by the terminal event, whose text is authoritative.
  message.content += text;
  return end;
}

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
          streamBytes: 0,
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
      case 'content.delta': {
        // Transient frame: never persisted. 第九轮补丁 3.2-A: the backend
        // stamps the SAME absolute UTF-8 end offset on deltas as on chunks,
        // so the gateway's reverse order (a buffered delta drained AFTER the
        // coalesced chunk replayed) is deduped by the shared byte ranges
        // instead of duplicating the answer.
        const payload = event.payload || {};
        if (payload.offset !== undefined) {
          message.streamBytes = applyIncrementalRange(message, payload);
          break;
        }
        // Legacy delta without an offset (pre-3.2 backend): append — the
        // old behaviour. The two sides must not have to switch in lockstep.
        const deltaText = payload.text || '';
        const deltaBefore = renderedBytes(message);
        message.content += deltaText;
        message.streamBytes = deltaBefore + utf8ByteLength(deltaText);
        break;
      }
      case 'content.chunk':
        // Persisted coalesced chunk. Since 第九轮 P1-4 the backend writes only
        // the INCREMENTAL text plus an end offset — carrying the cumulative
        // answer on every chunk made a run's durable event data quadratic in
        // the answer length. The `snapshot` branch is kept because historical
        // events (written before this round) still carry one, and for those
        // replace is self-healing.
        if (typeof event.payload?.snapshot === 'string') {
          message.content = event.payload.snapshot;
          message.streamBytes = utf8ByteLength(message.content);
          break;
        }
        message.streamBytes = applyIncrementalRange(message, event.payload || {});
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
        if (event.payload?.text) {
          message.content = event.payload.text;
          message.streamBytes = utf8ByteLength(message.content);
        }
        message.status = 'done';
        break;
      case 'run.failed':
        message.status = 'failed';
        message.error = event.payload?.error_message
          || event.payload?.error_code || '执行失败';
        break;
      case 'run.cancelled':
        // Hard Kill（应用/绑定被停用）：终态，但不是失败 —— 管理员主动
        // 撤销。折叠成 done 会让用户看到一个空白的“成功回答”。
        message.status = 'cancelled';
        message.error = cancelledNotice(event.payload?.error_code);
        break;
      case 'run.started':
        // 真正开始生成：清掉重试/暂缓提示，回到「生成中…」。
        message.retryNotice = undefined;
        break;
      case 'run.deferred':
        // 非终态：Provider 暂停或执行检查不可用，Run 被原优先级推迟。
        // 流保持打开，activeRunId 保持（与 run.retrying 同一语义）。
        if (message.status === 'streaming') {
          switch (event.payload?.reason) {
            case 'provider_disabled':
              message.retryNotice = '服务暂时停用，等待恢复…';
              break;
            case 'run_gate_unavailable':
              message.retryNotice = '执行检查暂不可用，正在等待重试…';
              break;
            default:
              message.retryNotice = '执行暂缓，正在等待重试…';
          }
        }
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
      case 'run.waiting_external':
        // NON-terminal park (第九轮 P0-2): the provider may already have
        // received the request and cannot be asked, so the run is suspended
        // rather than retried. The stream stays open, the run keeps its
        // conversation, and the backend resolves it (or fails it with
        // provider_submit_unknown) after a bounded grace window — so this is
        // a "waiting", never a silent infinite spinner with no explanation.
        if (message.status === 'streaming') {
          message.retryNotice = '请求结果待确认，正在等待外部响应…';
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

function consumeRunEvents(runId: string, generation: number) {
  const stream = openRunStream(runId, {
    onEvent: (event) => {
      if (!sessionStillCurrent(generation)) {
        // Abort and unregister a stream whose session ended while an event
        // was already queued. The guard is what covers the callback race;
        // the abort is what stops the transport reading further.
        stream.close();
        if (activeStreams.get(runId) === stream) activeStreams.delete(runId);
        return;
      }
      const state = useRunChatStore.getState();
      const patch = applyEvent(state, event);
      if (Object.keys(patch).length) useRunChatStore.setState(patch);
      if (TERMINAL.has(event.event_type)) {
        void finalizeRun(runId, event.payload?.text as string | undefined, generation);
      }
    },
    onError: () => {
      // Transport hiccup; the client auto-reconnects with full replay.
      // Nothing to clear: a stale stream is dropped by the onEvent guard.
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

// Exported for tests: the GET-Run reconciliation must not turn a
// cancelled run into a successful one (第四轮 P1-2).
//
// 第五轮 P1-1：GET Run + fetchRunArtifacts 都是异步请求，请求期间用户可能
// 已经发起了下一条 Run（Run B）。因此绝不能用请求前捕获的 conversation
// 快照整体覆盖回来 —— 那会把 B 的消息抹掉、或把 B 的 activeRunId 清成
// null。这里改为 functional setState：异步结束后基于**最新** store state
// 只 merge 我自己的那条消息。
export async function finalizeRun(
  runId: string,
  eventText?: string,
  generation = captureSessionGeneration(),
) {
  if (!sessionStillCurrent(generation)) return;
  closeRunStream(runId);
  let run: RunRecord;
  try {
    run = await getRun(runId);
  } catch {
    return;
  }
  if (!sessionStillCurrent(generation)) return;

  // Attach artifact cards from the run's persisted artifacts (source of
  // truth; covers artifacts discovered before the stream was opened).
  //
  // null 语义：本次查询失败 = "不知道"，不能覆盖事件流已经拿到的 artifact。
  // 只有真正拿到列表（哪怕是空数组）才写回。
  let fetchedArtifacts: ChatArtifact[] | null = null;
  try {
    const records: RunArtifactRecord[] = await fetchRunArtifacts(runId);
    fetchedArtifacts = records.map((r) => ({
      artifactId: r.id,
      name: r.name || '生成产物',
      normalizedType: r.normalized_type,
    }));
  } catch {
    fetchedArtifacts = null;
  }
  if (!sessionStillCurrent(generation)) return;

  useRunChatStore.setState((current) => {
    const cid = run.conversation;
    const conv = current.conversations[cid];
    if (!conv) return {};

    const messages = conv.messages.map((m) => {
      if (m.id !== `run-${runId}`) return m;
      // 'cancelled' 必须保持 cancelled：管理员撤销不是成功，也不是 Provider
      // 故障（第四轮 P1-2）。此前只有 failed 分支，cancelled 会被映射成
      // done —— 用户会看到一个空白的“成功回答”。
      const cancelled = run.status === 'cancelled';
      const failed = run.status === 'failed' || run.status === 'interrupted';
      // The reconciled text replaces whatever the stream accumulated, so the
      // byte counter has to follow it — a stale counter would make a later
      // replayed chunk look "already rendered" (or re-append it).
      const content = (eventText ?? run.output?.text) || m.content;
      return {
        ...m,
        content,
        streamBytes: utf8ByteLength(content),
        status: cancelled
          ? 'cancelled' as const
          : failed
            ? 'failed' as const
            : 'done' as const,
        error: cancelled
          ? (run.error_message || cancelledNotice(run.error_code))
          : failed
            ? (run.error_message || run.error_code || '执行失败')
            : undefined,
        artifacts: fetchedArtifacts !== null ? fetchedArtifacts : m.artifacts,
      };
    });

    return {
      conversations: {
        ...current.conversations,
        [cid]: {
          ...conv,
          messages,
          // 只能清理“我自己”。如果 B 已经成为 active run，A 绝不能把 B
          // 清掉（compare-and-clear）。
          activeRunId: conv.activeRunId === runId ? null : conv.activeRunId,
        },
      },
    };
  });

  refreshSidebarDebounced(generation);
}

// Live transcripts plus the pending-send idempotency identity, which belongs
// to the previous user's last failed action (P0-2).
registerSessionReset(() => useRunChatStore.getState().clearAll());
