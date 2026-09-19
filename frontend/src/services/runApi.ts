/**
 * v2 unified Run API client.
 *
 * The frontend only creates Runs and consumes their event stream —
 * execution belongs to the worker plane (architecture doc §55/§108).
 * No provider concepts (Aily/Codex/...) leak into this layer; provider
 * differences are encoded in runtime capabilities returned by the API.
 */
import { api } from '@/services/api';
import type { TaskSummary } from '@/types/task';

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

/**
 * The DISPLAY projection of one application (执行报告 §11, P2-1).
 *
 * What a card / switcher row / shortcut / bottom-sheet row needs, and nothing
 * else. `V2Application` is a structural SUPERTYPE of this, so a component that
 * only renders a row can accept `ApplicationSummary` and be handed either a
 * summary (workspace bootstrap) or a full catalog item (paged endpoint) —
 * which is exactly what the mobile sheet and the switcher do.
 *
 * `skills` / `capabilities` are deliberately absent: they exist on
 * `V2Application` and on the bootstrap's `default_application` (the one
 * application a composer is bound to).
 */
export type ConsumeBlockReason = 'disabled' | 'unbound' | 'runtime_unavailable';

export interface ApplicationSummary {
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
  category_slug?: string;
  category_name?: string;
  provider_key?: string;
  runtime_type?: string;
  /** 应用中心开关：停用的应用对普通用户完全隐藏（管理员仍可管理）。 */
  enabled?: boolean;
  /** False for fixed pages that have no runtime binding. */
  is_bound?: boolean;
  /** Backend SSOT: identical to catalog.Consumable / run admission. */
  is_consumable?: boolean;
  consume_block_reason?: ConsumeBlockReason;
  is_favorite?: boolean;
  /** Marked as the workspace's default main agent (§38). */
  is_default_agent?: boolean;
  /** Personal usage — drives 常用 / 最近使用 ordering in the home workspace. */
  usage_count?: number;
  last_used_at?: string | null;
  /** Global counter (Application.usage_count) — drives 推荐 only. */
  global_usage_count?: number;
}

/**
 * An application a COMPOSER can be bound to: the display projection plus the
 * two runtime facts the chat panel reads (技能 chips + the attachment
 * capability). This is exactly what the bootstrap's `default_application`
 * carries, and what `/applications/resolve` returns in full.
 */
export type ComposerApplication = ApplicationSummary & {
  skills?: AgentSkill[];
  capabilities?: Record<string, boolean>;
};

/**
 * `V2Application` — the CONSUMER shape (二次复审 P2-1).
 *
 * It deliberately does NOT carry the provider-authoring fields
 * (`external_resource_id`, `identity_mode`, `execution_mode`). Those are the
 * provider's own resource identity and belong to the authoring surface only
 * (`ManagedAgent` / `RuntimeAgentDetail`, returned by
 * GET /v2/applications/{id} and by create/update) — leaking them into every
 * paged list and every deep-link resolve gave every logged-in caller the
 * internal Aily agent ids.
 */
export interface V2Application extends ApplicationSummary {
  executor_key?: string;
  runtime_type: string;
  provider_key: string;
  capabilities: Record<string, boolean>;
  /** 是否公开到市场：私有（仅自己可见）只有管理员能看到。 */
  is_public?: boolean;
  /** True when the caller may edit / delete / re-avatar this application. */
  can_manage?: boolean;
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

// ---------------------------------------------------------------------------
// 工作台启动数据 (执行报告 §9–§14, P1-1/P1-2, 2026-09-17)
//
// The shell used to boot by downloading the WHOLE catalog
// (`GET /v2/applications`, once 1800+ rows) and projecting it in the browser:
// default agent, home shortcut groups, category rails, `@mention` candidates,
// slug lookups. Every one of those is now a server responsibility:
//
//   · GET /v2/workspace/bootstrap   → default agent + groups + category rails
//   · GET /v2/applications/resolve  → ONE application by slug or id
//   · GET /v2/applications/resolve-mention → the few `@` candidates
//   · GET /v2/applications/page     → the paged lists (see below)
//
// No studio surface may call the legacy whole-array endpoint again: a
// regression there is invisible in a small dev database and fatal in a large
// one. The backend keeps it (and meters it,
// studio_legacy_application_list_requests_total) only for non-studio clients.
// ---------------------------------------------------------------------------

/** One category rail entry (bootstrap). */
export interface ApplicationCategory {
  /** What `category_slug` accepts; `__uncategorized__` is the 其他 tab. */
  slug: string;
  name: string;
  count: number;
}

/**
 * The start-up payload: constant size, independent of the catalog size.
 *
 * `default_application` is the ONE application that carries `skills` and
 * `capabilities` (the home composer bound to it renders 技能 chips and gates
 * the attachment entry on the runtime capabilities); every other row is an
 * `ApplicationSummary`.
 */
export interface WorkspaceBootstrap {
  default_application: ComposerApplication | null;
  favorites: ApplicationSummary[];
  frequent: ApplicationSummary[];
  recent: ApplicationSummary[];
  recommended: ApplicationSummary[];
  recent_fixed_apps: ApplicationSummary[];
  recent_capabilities?: ApplicationSummary[];
  recent_tasks?: Array<{
    id: string;
    title: string;
    application_id: number | null;
    application_slug?: string;
    application_name?: string;
    application_icon?: string;
    application_color?: string;
    application_kind?: string;
    preview?: string;
    preview_role?: string;
    created_at: string;
    updated_at: string;
    execution_state: TaskSummary['executionState'];
  }>;
  agent_categories: ApplicationCategory[];
  app_categories: ApplicationCategory[];
}

export function fetchWorkspaceBootstrap(): Promise<WorkspaceBootstrap> {
  // ⚠️ The `/v2` prefix is LOAD-BEARING (二次复审 P0-1).
  //
  // The OpenAPI (SSOT) registers `GET /api/v2/workspace/bootstrap` and the
  // server mounts ONLY the generated routes (`genapi.HandlerFromMux`), so
  // there is no `/api/workspace/bootstrap` alias. Since `api` is an axios
  // instance with `baseURL = '/api'`, a path without `/v2` becomes
  // `/api/workspace/bootstrap` → 404. The store unit tests mock this
  // function away, which is exactly why the wrong path survived review —
  // see `runApi.contract.test.ts`, which asserts the real URL.
  return api.get<WorkspaceBootstrap>('/v2/workspace/bootstrap');
}

/**
 * Resolve exactly ONE application (执行报告 §12).
 *
 * The visibility policy is the catalog list's own, and an invisible or
 * unknown application answers 404 — so the caller treats "not found" and "not
 * allowed" the same way, which is what a deep link needs.
 */
export function resolveApplication(
  lookup: { slug?: string; id?: number },
): Promise<V2Application> {
  const params: Record<string, string> = {};
  if (lookup.slug) params.slug = lookup.slug;
  if (lookup.id != null) params.id = String(lookup.id);
  return api.get<V2Application>('/v2/applications/resolve', params);
}

/** One `@` candidate: `kind` decides open vs send (§14). */
export interface MentionCandidate {
  id: number;
  slug: string;
  name: string;
  kind: string;
}

/**
 * Server-side `@mention` resolution (执行报告 §14).
 *
 * Replaces the candidate array the shell used to build from the whole
 * catalog: the composer sends the token the user typed and gets back the few
 * applications whose name/slug could match, ranked.
 */
export async function resolveApplicationMention(
  q: string,
): Promise<MentionCandidate[]> {
  const needle = q.trim();
  if (!needle) return [];
  try {
    return await api.get<MentionCandidate[]>('/v2/applications/resolve-mention', { q: needle });
  } catch {
    // Routing is a convenience: an unreachable resolver must degrade to
    // "unknown mention → send as typed", never to a blocked composer (§38).
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
  is_consumable?: boolean;
  consume_block_reason?: ConsumeBlockReason;
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

// ---------------------------------------------------------------------------
// 服务端游标分页 (执行报告 §14–§24, 2026-09-17)
//
// GET /v2/applications/page evaluates LIMIT + the cursor condition inside
// SQL and aggregates personal usage / favourites for the returned page ids
// only. Since P1 it is the ONLY list endpoint any studio surface uses: the
// catalog mirror, the mention candidate array and the slug lookups all moved
// to /workspace/bootstrap + /applications/resolve (see above).
// ---------------------------------------------------------------------------

/** One keyset page of the application catalog. */
export interface ApplicationPage {
  items: V2Application[];
  /** Opaque keyset anchor; '' when has_more is false. */
  next_cursor: string;
  has_more: boolean;
}

export interface ApplicationPageQuery {
  /** 'chat' (market) | 'fixed' (应用中心, kind <> 'chat') | 'all'. */
  kind?: 'chat' | 'fixed' | 'all';
  scope?: 'accessible' | 'public' | 'manage' | 'mine';
  /**
   * `mode` answers a DIFFERENT question from `scope` (二次复审 P0-5).
   *
   *   scope = who may SEE the row   (management)
   *   mode  = may anyone actually USE it (consumption)
   *
   * `manage` (the backend default, omitted here) is the authoring surface:
   * staff also sees disabled / private / binding-less rows so they can
   * repair them. `consume` is every surface that OPENS or RUNS something
   * (切换器, 移动端目录, bootstrap 分组): it requires `enabled` and, for a
   * chat application, a runtime binding — the same gate the run API applies,
   * so the UI can no longer offer something the send will refuse.
   */
  mode?: 'manage' | 'consume';
  /** Keep binding-less rows (the marketplace needs them repairable). */
  includeUnbound?: boolean;
  /** Case-insensitive name/description substring, evaluated in SQL. */
  q?: string;
  /** Exact category slug; '__uncategorized__' matches rows with no category. */
  categorySlug?: string;
  /** Page size, 1..100 (default 24). */
  limit?: number;
  /** next_cursor from the previous page. */
  cursor?: string | null;
}

export async function fetchApplicationPage(
  options: ApplicationPageQuery = {},
): Promise<ApplicationPage> {
  const params: Record<string, string> = {};
  const kind = options.kind ?? 'chat';
  if (kind !== 'chat') params.kind = kind;
  if (options.scope && options.scope !== 'public') params.scope = options.scope;
  // `manage` is the backend default and is omitted so a management page's URL
  // stays identical to before; only a consumer surface asks for `consume`.
  if (options.mode && options.mode !== 'manage') params.mode = options.mode;
  if (options.includeUnbound) params.include_unbound = 'true';
  if (options.q && options.q.trim()) params.q = options.q.trim();
  if (options.categorySlug) params.category_slug = options.categorySlug;
  if (options.limit) params.limit = String(options.limit);
  if (options.cursor) params.cursor = options.cursor;
  return api.get<ApplicationPage>('/v2/applications/page', params);
}

export function fetchAgentRuntimes(): Promise<AgentRuntimeDescriptor[]> {
  return api.get<AgentRuntimeDescriptor[]>('/v2/runtimes');
}

export interface FixedApplicationPayload {
  name: string;
  slug?: string;
  description?: string;
  icon?: string;
  color?: string;
  kind?: 'page' | 'form' | 'dashboard' | 'custom' | 'task';
  renderer_key?: string;
}

export function createFixedApplication(
  payload: FixedApplicationPayload,
): Promise<ManagedAgent> {
  return api.post<ManagedAgent>('/v2/applications', { ...payload, is_public: false });
}

export function updateFixedApplication(
  applicationId: number,
  payload: Partial<FixedApplicationPayload> & { enabled?: boolean },
): Promise<ManagedAgent> {
  return api.patch<ManagedAgent>(`/v2/applications/${applicationId}`, payload);
}

export function createAgentApplication(
  payload: AgentApplicationPayload,
): Promise<ManagedAgent> {
  return api.post<ManagedAgent>('/v2/applications', payload);
}

/**
 * The AUTHORING detail read (三次复审 P0-R4.1): the backend answers this
 * only to staff — it carries the provider-authoring fields
 * (`external_resource_id`, `identity_mode`, `execution_mode`). Consumers
 * must go through `resolveApplication` / `fetchApplicationPage` /
 * `fetchWorkspaceBootstrap`, whose consumer DTO omits those fields.
 *
 * Centralized here (P2-R2): this module is the ONLY place allowed to write
 * an applications URL; the ESLint `no-restricted-syntax` rule enforces it.
 */
export function fetchApplicationDetail(
  applicationId: number,
): Promise<RuntimeAgentDetail> {
  return api.get<RuntimeAgentDetail>(`/v2/applications/${applicationId}`);
}

/** The compact authoring shape `GET /v2/applications/{id}` answers with. */
export interface RuntimeAgentDetail {
  name: string;
  slug: string;
  description: string;
  icon: string;
  category_slug?: string;
  is_public: boolean;
  is_default_agent?: boolean;
  runtime_type?: string;
  provider_key?: string;
  external_resource_id?: string;
  identity_mode?: string;
  execution_mode?: string;
  /** 技能配置, maintained by the agent editor form (§9). */
  skills?: AgentSkill[];
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
