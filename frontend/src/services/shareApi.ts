/**
 * Conversation share API — server-side snapshot sharing.
 *
 * A share pins the selected message ids on the backend under a random token;
 * the public read endpoint returns ONLY those messages. Never build share
 * links that merely name message ids in the URL — that was the reference
 * implementation's flaw (stripping the suffix revealed the whole
 * conversation because filtering happened client-side).
 */
import { api } from './api';

export interface ConversationShare {
  share_token: string;
  message_count: number;
  created_at: string;
}

export interface PublicShareArtifact {
  artifact_id: string;
  name: string;
}

export interface PublicShareMessage {
  role: 'user' | 'assistant' | 'system';
  content: string;
  created_at: string;
  /** Metadata-only artifact references (resolver via the token-scoped /open). */
  artifacts?: PublicShareArtifact[];
}

export interface PublicShareDetail {
  title: string;
  shared_at: string;
  messages: PublicShareMessage[];
}

export const createConversationShare = (
  conversationId: number,
  messageIds: number[],
): Promise<ConversationShare> =>
  api.post<ConversationShare>(`/v2/conversations/${conversationId}/shares`, {
    message_ids: messageIds,
  });

export const revokeConversationShare = (
  conversationId: number,
  shareToken: string,
): Promise<{ detail: string }> =>
  api.delete(`/v2/conversations/${conversationId}/shares/${shareToken}`);

export const getPublicShare = (shareToken: string): Promise<PublicShareDetail> =>
  api.get<PublicShareDetail>(`/v2/public/shares/${shareToken}`);

/** Share links stay on this origin — the receiver opens the SPA directly. */
export const buildShareUrl = (shareToken: string): string =>
  `${window.location.origin}/share/${shareToken}`;

// ── Feishu forwarding ────────────────────────────────────────────────────

export interface FeishuForwardTarget {
  id: string;
  name: string;
  avatar_url: string;
  target_type: 'user' | 'chat';
}

export interface FeishuForwardResult {
  results: { target_id: string; ok: boolean; error?: string }[];
  success_count: number;
  fail_count: number;
}

export const fetchFeishuTargets = (
  type: 'user' | 'chat',
  query?: string,
): Promise<FeishuForwardTarget[]> =>
  api.get<FeishuForwardTarget[]>('/v2/feishu/forward/targets', { type, query });

export const forwardShareToFeishu = (
  shareToken: string,
  targets: { target_type: 'user' | 'chat'; id: string }[],
): Promise<FeishuForwardResult> =>
  api.post<FeishuForwardResult>('/v2/feishu/forward', {
    share_token: shareToken,
    targets,
  });

/**
 * Recent forward targets (localStorage, newest first, capped) for one-tap
 * re-selection — mirrors the reference implementation's 最近转发.
 */
const FORWARD_HISTORY_KEY = 'feishu_forward_history';

export const loadForwardHistory = (): FeishuForwardTarget[] => {
  try {
    const raw = localStorage.getItem(FORWARD_HISTORY_KEY);
    const parsed = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? parsed.slice(0, 10) : [];
  } catch {
    return [];
  }
};

export const saveForwardHistory = (targets: FeishuForwardTarget[]): void => {
  const merged = [
    ...targets,
    ...loadForwardHistory().filter(
      (t) => !targets.some((n) => n.id === t.id),
    ),
  ];
  try {
    localStorage.setItem(
      FORWARD_HISTORY_KEY,
      JSON.stringify(merged.slice(0, 10)),
    );
  } catch {
    // localStorage unavailable — history is best-effort.
  }
};
