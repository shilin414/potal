/**
 * Participant identity for the chat surface.
 *
 * Every message shows WHO said it: the human as `姓名（user_id）` with their
 * own avatar, the agent as its application name (创作助手 / 问数小安 …) with
 * its avatar — never the generic 你 / 助手 labels (§46 Unified User).
 *
 * Pure functions on purpose: the run panel and the legacy message list must
 * label participants identically, and the fallbacks are unit tested.
 */

/** The user shape the auth store persists (login / OAuth exchange payload). */
export interface ChatUserIdentity {
  username?: string | null;
  display_name?: string | null;
  display_id?: string | number | null;
  /** Feishu CDN avatar mirrored onto User.avatar. */
  avatar?: string | null;
  /** Server-side avatar URL (identity snapshot); preferred over `avatar`. */
  avatar_url?: string | null;
}

/** The agent shape: an Application as returned by GET /api/v2/applications. */
export interface ChatAgentIdentity {
  name?: string | null;
  /** Emoji fallback used while no avatar image is uploaded. */
  icon?: string | null;
  avatar_url?: string | null;
}

/** Human-readable name: display_name → username → 我. */
export function userDisplayName(user?: ChatUserIdentity | null): string {
  const name = (user?.display_name || user?.username || '').trim();
  return name || '我';
}

/**
 * The id shown in parentheses. The backend always resolves one (Feishu
 * user_id → operator-set display_id → local pk), so this stays empty only for
 * anonymous/legacy payloads — in that case we render the name alone instead of
 * an empty pair of brackets.
 */
export function userDisplayId(user?: ChatUserIdentity | null): string {
  const raw = user?.display_id;
  if (raw === null || raw === undefined) return '';
  return String(raw).trim();
}

/** `吴志彬（19127920）`; falls back to the bare name when no id is known. */
export function formatUserLabel(user?: ChatUserIdentity | null): string {
  const name = userDisplayName(user);
  const id = userDisplayId(user);
  return id ? `${name}（${id}）` : name;
}

/** Avatar image URL of the human ('' when unknown → initials are rendered). */
export function userAvatarUrl(user?: ChatUserIdentity | null): string {
  return (user?.avatar_url || user?.avatar || '').trim();
}

/** Initial rendered inside the avatar when there is no image. */
export function userAvatarFallback(user?: ChatUserIdentity | null): string {
  const name = userDisplayName(user);
  return name.slice(0, 1).toUpperCase();
}

/** Agent name: the application name, never the generic word 助手. */
export function agentDisplayName(agent?: ChatAgentIdentity | null): string {
  return (agent?.name || '').trim() || '智能体';
}

/** Agent avatar image URL ('' → the emoji icon is rendered instead). */
export function agentAvatarUrl(agent?: ChatAgentIdentity | null): string {
  return (agent?.avatar_url || '').trim();
}

/** Fallback content of the agent avatar: its emoji icon, else AI. */
export function agentAvatarFallback(agent?: ChatAgentIdentity | null): string {
  return (agent?.icon || '').trim() || 'AI';
}
