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
import {
  captureSessionGeneration,
  sessionStillCurrent,
} from '@/stores/resetSessionState';

export interface SessionUser {
  id?: string | number;
  username?: string;
  email?: string;
  role?: string;
  created_at?: string;
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
  const initial = useAuthStore.getState();
  if (!initial.isAuthenticated || !initial.user) return null;
  // Both fences are required (四次复审 P0-R6): the epoch rejects a response
  // from a session that ended, and the id comparison rejects a response that
  // belongs to a DIFFERENT account installed in the same session slot.
  const expectedUserId = String(initial.user.id);
  const generation = captureSessionGeneration();
  const session = await fetchSessionUser();
  if (!session?.username || !sessionStillCurrent(generation)) return null;
  const current = useAuthStore.getState();
  if (
    !current.isAuthenticated
    || !current.user
    || String(current.user.id) !== expectedUserId
    || (session.id != null && String(session.id) !== expectedUserId)
  ) {
    return null;
  }
  // updateUser merges, but an explicit `undefined` would still clobber the
  // previous value — hence the `??` guards.
  current.updateUser({
    username: session.username,
    display_name: session.display_name ?? current.user.display_name,
    display_id: session.display_id ?? current.user.display_id,
    avatar_url: session.avatar_url || current.user.avatar_url,
    auth_source: session.auth_source ?? current.user.auth_source,
    is_staff: session.is_staff ?? current.user.is_staff,
  } as any);
  return session;
}
/**
 * Revalidate a user restored from localStorage before mounting the private UI.
 *
 * The HttpOnly cookie is the authoritative identity. localStorage can outlive
 * that cookie or belong to the account that was signed in previously, so a
 * persisted `is_staff=true` must never render privileged navigation while the
 * session request is still in flight. Downgrade first, then let the server
 * snapshot restore staff authority after it has been verified.
 *
 * Verification fails closed: if the server identity cannot be established,
 * local authentication and every session-scoped store are cleared rather than
 * mounting private UI with data that may belong to another account.
 */
let bootstrapAttempt = 0;
let bootstrapFlight: Promise<void> | null = null;

async function runPersistedSessionBootstrap(attempt: number): Promise<void> {
  const initial = useAuthStore.getState();
  if (!initial.isAuthenticated || !initial.user) return;

  const expectedUserId = String(initial.user.id);
  const generation = captureSessionGeneration();
  initial.updateUser({ is_staff: false });

  const session = await fetchSessionUser();
  if (attempt !== bootstrapAttempt) return;

  const current = useAuthStore.getState();
  if (
    !current.isAuthenticated
    || !current.user
    || String(current.user.id) !== expectedUserId
    || !sessionStillCurrent(generation)
  ) {
    return;
  }
  if (!session?.username) {
    current.clearAuth();
    return;
  }

  const sessionUserId = String(session.id ?? expectedUserId);
  const sameIdentity = sessionUserId === expectedUserId;
  current.acceptAuthenticatedUser({
    ...(sameIdentity ? current.user : {}),
    id: sessionUserId,
    username: session.username,
    email: session.email ?? (sameIdentity ? current.user.email : ''),
    role: session.role ?? (session.is_staff ? 'admin' : 'user'),
    avatar_url: session.avatar_url || (sameIdentity ? current.user.avatar_url : undefined),
    display_name: session.display_name ?? (sameIdentity ? current.user.display_name : session.username),
    display_id: session.display_id ?? (sameIdentity ? current.user.display_id : ''),
    auth_source: session.auth_source ?? (sameIdentity ? current.user.auth_source : ''),
    is_staff: Boolean(session.is_staff),
    created_at: session.created_at ?? (sameIdentity ? current.user.created_at : ''),
  });
}

function startPersistedSessionBootstrap(force: boolean): Promise<void> {
  if (!force && bootstrapFlight) return bootstrapFlight;

  const attempt = ++bootstrapAttempt;
  const promise = runPersistedSessionBootstrap(attempt);
  bootstrapFlight = promise;
  void promise.then(
    () => { if (bootstrapFlight === promise) bootstrapFlight = null; },
    () => { if (bootstrapFlight === promise) bootstrapFlight = null; },
  );
  return promise;
}

/** Start a new authoritative verification and supersede any older response. */
export function bootstrapPersistedSession(): Promise<void> {
  return startPersistedSessionBootstrap(true);
}

/** Share one request across React StrictMode's duplicate mount effect. */
export function bootstrapPersistedSessionOnce(): Promise<void> {
  return startPersistedSessionBootstrap(false);
}
