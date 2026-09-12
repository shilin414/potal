/**
 * @Application 指令路由 (架构文档 §36-§38).
 *
 * The workspace composer accepts `@销售助手 帮我分析一下这个客户`:
 *
 *   解析 Application → 切换 active_application → 保留原始指令 → 发送
 *
 * `@修改OA密码` (a non-chat application) opens its workspace instead of
 * sending anything (§37). An unknown `@...` is NOT an error — it falls
 * through to the default main agent with the text untouched (§38).
 *
 * Pure functions only: no React, no requests. That keeps the routing rules
 * unit testable and keeps provider/UI concerns out of the parse step.
 */

export interface MentionCandidate {
  id: number;
  slug: string;
  name: string;
  /** 'chat' applications can receive the instruction; others are opened. */
  kind: string;
}

export interface MentionMatch {
  application: MentionCandidate;
  /** The instruction with the @mention removed, rest preserved verbatim. */
  content: string;
}

export interface MentionParse {
  /** True when the text starts with an `@` token (matched or not). */
  mentioned: boolean;
  /** Set when the `@` token resolved to an application. */
  match: MentionMatch | null;
}

export type WorkspaceRoute =
  /** No active mention: send the text as typed in the current workspace. */
  | { action: 'passthrough' }
  /** Mention stripped; send the remaining instruction here. */
  | { action: 'send'; content: string }
  /** Carry the instruction to another application, then send it there. */
  | { action: 'switch'; applicationId: number; content: string }
  /** Fixed page / task: open its workspace (§37), nothing to send. */
  | { action: 'open'; applicationId: number }
  /** Bare mention of the current application: nothing to send. */
  | { action: 'ignore' };

const isBoundary = (text: string, index: number): boolean => (
  index >= text.length || /\s/.test(text[index])
);

/**
 * Longest-first name lookup so `@销售助手` cannot be shadowed by a shorter
 * application whose name is a prefix of it.
 */
function matchByPrefix(
  body: string,
  candidates: MentionCandidate[],
): MentionCandidate | null {
  const ordered = [...candidates].sort((a, b) => b.name.length - a.name.length);
  const lower = body.toLowerCase();
  for (const candidate of ordered) {
    if (!candidate.name) continue;
    const name = candidate.name.toLowerCase();
    if (lower.startsWith(name) && isBoundary(body, candidate.name.length)) {
      return candidate;
    }
  }
  return null;
}

function matchByToken(
  body: string,
  candidates: MentionCandidate[],
): MentionCandidate | null {
  const token = body.split(/\s/)[0];
  if (!token) return null;
  const lower = token.toLowerCase();
  return candidates.find((candidate) => (
    candidate.slug.toLowerCase() === lower
    || candidate.name.toLowerCase() === lower)) || null;
}

/**
 * Parse a leading `@mention`.
 *
 * Only a LEADING mention routes: `这是 @销售助手` stays a normal message
 * (emails and `@` used inline are untouched).
 */
export function parseMention(
  text: string,
  candidates: MentionCandidate[],
): MentionParse {
  const trimmed = text.replace(/^\s+/, '');
  if (!trimmed.startsWith('@')) return { mentioned: false, match: null };
  const body = trimmed.slice(1);
  const application = matchByPrefix(body, candidates)
    || matchByToken(body, candidates);
  if (!application) return { mentioned: true, match: null };
  // Strip exactly the mention (name or slug token) and keep the REST as the
  // user wrote it — the agent must receive the original instruction.
  const matchedLength = body.toLowerCase().startsWith(application.name.toLowerCase())
    ? application.name.length
    : body.split(/\s/)[0].length;
  const content = body.slice(matchedLength).replace(/^\s+/, '');
  return { mentioned: true, match: { application, content } };
}

export interface RouteContext {
  text: string;
  candidates: MentionCandidate[];
  /**
   * The application the composer currently sends to. On the home workspace
   * this is the default chat application, so a bare message needs no switch.
   */
  activeApplicationId: number | null;
}

/** Turn composer text into a workspace action. Never throws. */
export function routeMention({
  text,
  candidates,
  activeApplicationId,
}: RouteContext): WorkspaceRoute {
  const { match } = parseMention(text, candidates);
  if (!match) {
    // Unknown @ (or none at all): the default main agent handles the raw
    // text — an unresolvable mention must never surface an error (§38).
    return { action: 'passthrough' };
  }

  const { application, content } = match;
  if (application.kind !== 'chat') {
    return { action: 'open', applicationId: application.id };
  }

  const isActive = activeApplicationId === application.id;
  if (!content) {
    // A bare `@this-app` is a focus request, not a message.
    return isActive
      ? { action: 'ignore' }
      : { action: 'switch', applicationId: application.id, content: '' };
  }
  return isActive
    ? { action: 'send', content }
    : { action: 'switch', applicationId: application.id, content };
}
