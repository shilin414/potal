/**
 * Studio session sync.
 *
 * `GET /api/identity/session` is the authoritative snapshot of the signed-in
 * user (display_name / display_id / avatar_url). The auth store is persisted in
 * localStorage, so a session restored from an older build — or one whose
 * profile changed server-side (new avatar, operator-set user_id) — would keep
 * showing stale labels in the chat.
 *
 * Best-effort by design: a failure leaves the persisted user untouched (the
 * request may simply 401 when logged out), and it never throws at the caller.
 */
import axiosInstance from './axios';
import { useAuthStore } from '@/stores/useAuthStore';

export interface SessionUser {
  id?: string | number;
  username?: string;
  display_name?: string;
  display_id?: string;
  avatar_url?: string;
  auth_source?: string;
  is_staff?: boolean;
}

export async function fetchSessionUser(): Promise<SessionUser | null> {
  try {
    const data = await axiosInstance.get('/identity/session') as any;
    return data && typeof data === 'object' ? data as SessionUser : null;
  } catch {
    return null;
  }
}

/**
 * Merge the server session into the persisted user. Only fields the session
 * endpoint owns are overwritten, so local-only bits (email/role on the login
 * payload) survive.
 */
export async function syncSessionUser(): Promise<SessionUser | null> {
  const { user, isAuthenticated } = useAuthStore.getState();
  if (!isAuthenticated) return null;
  const session = await fetchSessionUser();
  if (!session?.username) return null;
  // updateUser merges, but an explicit `undefined` would still clobber the
  // previous value — hence the `??` guards.
  useAuthStore.getState().updateUser({
    username: session.username,
    display_name: session.display_name ?? user?.display_name,
    display_id: session.display_id ?? user?.display_id,
    avatar_url: session.avatar_url || user?.avatar_url,
    auth_source: session.auth_source ?? user?.auth_source,
    is_staff: session.is_staff ?? user?.is_staff,
  } as any);
  return session;
}
