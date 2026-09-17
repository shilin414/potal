/**
 * v2 unified Run API client.
 *
 * The frontend only creates Runs and consumes their event stream —
 * execution belongs to the worker plane (architecture doc §55/§108).
 * No provider concepts (Aily/Codex/...) leak into this layer; provider
 * differences are encoded in runtime capabilities returned by the API.
 */
import { api } from '@/services/api';

export interface RunRecord {
  id: string;
  application: number;
  conversation: number;
  provider: string;
  runtime_type: string;
  status: 'queued' | 'running' | 'waiting_input' | 'waiting_external'
    | 'cancelling' | 'cancelled' | 'succeeded' | 'failed' | 'interrupted';
  output?: { text?: string } | null;
  error_code?: string;
  error_message?: string;
  created_at: string;
  /**
   * Idempotency envelope (第九轮 P0-1). Present only on a create request that
   * carried a `client_request_id`: `idempotency_replayed` marks a response
   * that returned an ALREADY existing run instead of creating one.
   */
  client_request_id?: string;
  idempotency_replayed?: boolean;
}

export interface RunArtifactRecord {
  id: string;
  run: string;
  name: string;
  normalized_type: string;
  provider_artifact_type: string;
  created_at: string;
}

export interface V2Application {
  id: number;
  slug: string;
  name: string;
  description: string;
  icon: string;
  color?: string;
  /** Auth-checked avatar URL ('' when the agent still uses its emoji icon). */
  avatar_url?: string;
  /** Application kind: 'chat' | 'task' | 'custom'. */
  kind: string;
  renderer_key?: string;
  executor_key?: string;
  category_slug?: string;
  category_name?: string;
  runtime_type: string;
  provider_key: string;
  /** Aily agent_id (or the provider's equivalent resource id). */
  external_resource_id?: string;
  identity_mode: string;
  execution_mode: string;
  capabilities: Record<string, boolean>;
  /** False for fixed pages that have no runtime binding. */
  is_bound?: boolean;
  is_favorite?: boolean;
  /** 是否公开到市场：私有（仅自己可见）只有管理员能看到。 */
  is_public?: boolean;
  /** 应用中心开关：停用的应用对普通用户完全隐藏（管理员仍可管理）。 */
  enabled?: boolean;
  /** Marked as the workspace's default main agent (§38). */
  is_default_agent?: boolean;
  /** True when the caller may edit / delete / re-avatar this application. */
  can_manage?: boolean;
  /** Personal usage — drives 常用 / 最近使用 ordering in the home workspace. */
  usage_count?: number;
  last_used_at?: string | null;
  /** Global counter (Application.usage_count) — drives 推荐 only. */
  global_usage_count?: number;
  /**
   * 技能配置 — the agent's own skills, configured in 智能体市场.
   *
   * The backend always sends this (`[]` when the agent has none), so a
   * selector can tell "none configured" apart from "not loaded". Optional in
   * the type only so older cached payloads and test fixtures stay valid.
   */
  skills?: AgentSkill[];
}

/**
 * An agent-scoped skill.
 *
 * A skill is a NAMED PROMPT FRAGMENT the composer prepends to the user's
 * message — NOT a provider capability. The provider (Aily or otherwise) only
 * ever receives ordinary message content; interpreting the fragment is the
 * custom agent's own business. That is why selecting a skill needs no new Run
 * field: it changes `content`, which the existing idempotency hash already
 * covers.
 */
export interface AgentSkill {
  /** Stable across renames, so a saved selection survives a reworded skill. */
  id: string;
  /** Composer chip label (「查询收入数据」). */
  name: string;
  /** One-line subtitle in the skill sheet. */
  description: string;
  /** Prepended verbatim to the user's message (「/查询收入查询」). */
  prompt: string;
}

/**
 * A skill as SUBMITTED by the authoring form.
 *
 * `id` may be omitted: the backend derives a stable one from the name, which is
 * what lets a newly added skill work without the form inventing identifiers.
 * An existing skill's id is sent back unchanged so its reworded text does not
 * invalidate anyone's saved composer selection.
 */
export type AgentSkillInput = Omit<AgentSkill, 'id'> & { id?: string };

export interface UploadPendingAttachment {
  id: string;
  name: string;
  size?: number;
  attachment_type: string;
}

/** Official Aily attachment limits (custom-agent docs: 附件/上传附件). */
export const ATTACHMENT_LIMITS = {
  maxPerRun: 8,
  imageMaxBytes: 5 * 1024 * 1024,
  fileMaxBytes: 40 * 1024 * 1024,
  allowedExtensions: ['png', 'jpg', 'jpeg', 'pdf'] as string[],
} as const;

export interface AttachmentViolation {
  name: string;
  reason: string;
}

/** Client-side pre-check mirroring the provider's official constraints. */
export function validateAttachment(file: File): AttachmentViolation | null {
  const ext = (file.name.split('.').pop() || '').toLowerCase();
  if (!ATTACHMENT_LIMITS.allowedExtensions.includes(ext)) {
    return { name: file.name, reason: '仅支持 png / jpg / pdf 格式' };
  }
  const isImage = ext === 'png' || ext === 'jpg' || ext === 'jpeg';
  if (isImage && file.size > ATTACHMENT_LIMITS.imageMaxBytes) {
    return { name: file.name, reason: '图片不能超过 5MB' };
  }
  if (!isImage && file.size > ATTACHMENT_LIMITS.fileMaxBytes) {
    return { name: file.name, reason: '文件不能超过 40MB' };
  }
  return null;
}

/** Openable applications (default: chat apps, i.e. the default-app resolver).
 *
 * `kind` widens the listing for the workspace home shortcuts: 'all' returns
 * chat apps (bound) plus fixed pages / tasks, so the shell can render every
 * shortcut from one request (architecture doc §35).
 *
 * `scope` widens *whose* applications come back: 'public' (default) keeps the
 * original semantics, 'manage' adds the caller's own (private included) so a
 * user's own agents appear in the switcher and in 智能体市场.
 * `includeUnbound` also returns chat apps whose binding is missing/disabled —
 * the marketplace needs them to stay visible and repairable.
 */
export async function fetchV2Applications(
  kind: 'chat' | 'task' | 'custom' | 'all' = 'chat',
  options: {
    scope?: 'public' | 'manage' | 'mine';
    includeUnbound?: boolean;
  } = {},
): Promise<V2Application[]> {
  const params: Record<string, string> = {};
  if (kind !== 'chat') params.kind = kind;
  if (options.scope && options.scope !== 'public') params.scope = options.scope;
  if (options.includeUnbound) params.include_unbound = 'true';
  try {
    return await api.get<V2Application[]>(
      '/v2/applications', Object.keys(params).length ? params : undefined);
  } catch {
    return [];
  }
}

// ---------------------------------------------------------------------------
// 智能体市场 authoring (新建 / 编辑 / 头像 / 主智能体)
//
// Provider differences are never hard-coded here: the create form renders
// itself from `fetchAgentRuntimes()`, which the backend builds out of the
// registered RuntimeAdapters (§16).
// ---------------------------------------------------------------------------

/** An application as returned by the authoring endpoints. */
export interface ManagedAgent {
  id: number;
  slug: string;
  name: string;
  description: string;
  icon: string;
  avatar_url: string;
  color?: string;
  kind: string;
  is_public: boolean;
  enabled?: boolean;
  category_slug: string;
  category_name: string;
  is_default_agent: boolean;
  is_bound: boolean;
  runtime_type: string;
  provider_key: string;
  external_resource_id: string;
  identity_mode: string;
  execution_mode: string;
  can_manage: boolean;
  updated_at?: string;
  /** Present on the listing variant of the payload (智能体市场 cards). */
  usage_count?: number;
  last_used_at?: string | null;
  is_favorite?: boolean;
  capabilities?: Record<string, boolean>;
  /** 技能配置, as configured in 智能体市场 (see AgentSkill). */
  skills?: AgentSkill[];
}

/** A runtime an agent can be built on (one Provider × registered adapter). */
export interface AgentRuntimeDescriptor {
  key: string;
  provider_key: string;
  provider_name: string;
  runtime_type: string;
  /** e.g. 「飞书 Aily 自定义智能体」 */
  label: string;
  resource_id_label: string;
  resource_id_hint: string;
  resource_id_required: boolean;
  /** Optional client-side pre-check (regex source) — the same one the API uses. */
  resource_id_pattern: string;
  identity_modes: string[];
  execution_modes: string[];
  capabilities: Record<string, boolean>;
}

export interface AgentRuntimePayload {
  provider_key: string;
  runtime_type: string;
  external_resource_id?: string;
  identity_mode?: string;
  execution_mode?: string;
  timeout_seconds?: number;
}

export interface AgentApplicationPayload {
  name: string;
  slug?: string;
  description?: string;
  icon?: string;
  color?: string;
  category_slug?: string;
  category_name?: string;
  is_public?: boolean;
  runtime?: AgentRuntimePayload;
  set_default_agent?: boolean;
  /**
   * 技能配置. The whole list is replaced on save, matching the backend
   * contract; OMITTING the field leaves the stored skills untouched (which is
   * what a rename-only PATCH must do), while `[]` clears them.
   */
  skills?: AgentSkillInput[];
}

export interface RuntimeValidationResult {
  ok: boolean;
  /** False when the check could not be attempted (no provider identity...). */
  checked: boolean;
  detail: string;
}

/** Agents the caller can manage: public ones + their own, bound or not. */
export async function fetchManageableAgents(): Promise<ManagedAgent[]> {
  try {
    return await api.get<ManagedAgent[]>('/v2/applications', {
      kind: 'chat', scope: 'manage', include_unbound: 'true',
    });
  } catch {
    return [];
  }
}

export function fetchAgentRuntimes(): Promise<AgentRuntimeDescriptor[]> {
  return api.get<AgentRuntimeDescriptor[]>('/v2/runtimes');
}

export function createAgentApplication(
  payload: AgentApplicationPayload,
): Promise<ManagedAgent> {
  return api.post<ManagedAgent>('/v2/applications', payload);
}

export function updateAgentApplication(
  applicationId: number,
  // `enabled`（应用中心开关）只存在于更新：创建时固定为启用。
  payload: Partial<AgentApplicationPayload> & { enabled?: boolean },
): Promise<ManagedAgent> {
  return api.patch<ManagedAgent>(`/v2/applications/${applicationId}`, payload);
}

export function deleteAgentApplication(applicationId: number): Promise<void> {
  return api.delete(`/v2/applications/${applicationId}`);
}

export function uploadAgentAvatar(
  applicationId: number,
  file: File,
): Promise<ManagedAgent> {
  const form = new FormData();
  form.append('file', file);
  return api.post<ManagedAgent>(
    `/v2/applications/${applicationId}/avatar`, form);
}

export function clearAgentAvatar(applicationId: number): Promise<ManagedAgent> {
  return api.delete<ManagedAgent>(`/v2/applications/${applicationId}/avatar`);
}

/** Promote / demote the workspace main agent (architecture doc §38). */
export function setDefaultAgent(
  applicationId: number,
  isDefault: boolean,
): Promise<ManagedAgent> {
  return isDefault
    ? api.post<ManagedAgent>(`/v2/applications/${applicationId}/default-agent`)
    : api.delete<ManagedAgent>(`/v2/applications/${applicationId}/default-agent`);
}

/** Best-effort pre-flight: does the caller actually see this agent? */
export function validateAgentRuntime(
  payload: AgentRuntimePayload,
): Promise<RuntimeValidationResult> {
  return api.post<RuntimeValidationResult>('/v2/runtimes/validate', payload);
}

/** Pin / unpin an application for the current user (§35 收藏). Idempotent. */
export function setApplicationFavorite(
  applicationId: number,
  favorite: boolean,
): Promise<{ application_id: number; is_favorite: boolean }> {
  return favorite
    ? api.post(`/v2/applications/${applicationId}/favorite`)
    : api.delete(`/v2/applications/${applicationId}/favorite`);
}

/**
 * A fresh idempotency identity for ONE SEND ACTION (第九轮 P0-1).
 *
 * The caller owns this value's lifecycle, and the rule is about USER
 * actions, not HTTP attempts:
 *
 *   user hits send            → one id
 *   transport retry of that   → SAME id (a replay, not a second turn)
 *   user edits and sends again→ new id
 *
 * That distinction is the whole point: a second id would create a second
 * run, a second user message and a second provider chat for what the user
 * experienced as one message.
 *
 * `crypto.randomUUID` needs a secure context, so a v4-shaped id is built
 * from `getRandomValues` when the page is served over plain http (a LAN
 * host or an IP origin) — the backend treats the value as opaque and only
 * caps its length at 64.
 */
export function newClientRequestId(): string {
  const c = globalThis.crypto;
  if (c && typeof c.randomUUID === 'function') return c.randomUUID();
  const bytes = new Uint8Array(16);
  if (c && typeof c.getRandomValues === 'function') {
    c.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i += 1) bytes[i] = Math.floor(Math.random() * 256);
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x40; // version 4
  bytes[8] = (bytes[8] & 0x3f) | 0x80; // variant 10
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export function createRun(
  applicationId: number,
  content: string,
  options: {
    conversationId?: number | null;
    attachmentIds?: string[];
    /**
     * Idempotency token for this send action (see newClientRequestId). When
     * omitted the request is NOT idempotent and a lost response can produce
     * a duplicate turn — callers that can be retried must pass one.
     */
    clientRequestId?: string;
  } = {},
): Promise<RunRecord> {
  return api.post<RunRecord>('/v2/runs', {
    application_id: applicationId,
    ...(options.conversationId ? { conversation_id: options.conversationId } : {}),
    content,
    attachment_ids: options.attachmentIds || [],
    ...(options.clientRequestId ? { client_request_id: options.clientRequestId } : {}),
  });
}

export function getRun(runId: string): Promise<RunRecord> {
  return api.get<RunRecord>(`/v2/runs/${runId}`);
}

/** One keyset page of a run's persisted events (第八轮→第九轮 P1-3). */
export interface RunEventPage {
  items: RunEventRecord[];
  /** Highest sequence in this page; feed back as `after` to continue. */
  next_after: number;
  /** True when the page was full, so more events may follow. */
  has_more: boolean;
}

export function fetchRunEvents(
  runId: string,
  after = 0,
  limit?: number,
): Promise<RunEventPage> {
  return api.get<RunEventPage>(`/v2/runs/${runId}/events`, {
    after,
    ...(limit ? { limit } : {}),
  });
}

export function fetchRunArtifacts(runId: string): Promise<RunArtifactRecord[]> {
  return api.get<RunArtifactRecord[]>(`/v2/runs/${runId}/artifacts`);
}

/** Provider attachment upload before the Run exists (§68: upload & chat
 *  must share one auth context — the backend pins both to this user). */
export async function uploadAttachment(
  applicationId: number,
  file: File,
): Promise<UploadPendingAttachment> {
  const form = new FormData();
  form.append('file', file);
  form.append('type', file.type.startsWith('image/') ? 'image' : 'file');
  const record = await api.post<RuntimeAttachmentRecord>(
    `/v2/applications/${applicationId}/attachments`, form,);
  return {
    id: record.id,
    name: record.name,
    size: file.size,
    attachment_type: record.attachment_type,
  };
}

interface RuntimeAttachmentRecord {
  id: string;
  attachment_type: string;
  name: string;
}

// Unified event protocol record (RunEvent rows / SSE frames).
export interface RunEventRecord {
  run_id?: string;
  sequence?: number;
  event_type: string;
  payload: Record<string, any>;
  created_at?: string;
}
