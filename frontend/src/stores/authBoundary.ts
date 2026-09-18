const AUTH_CHANNEL_NAME = 'studio-auth';
const AUTH_STORAGE_KEY = 'studio-auth-event';
const EXPLICIT_LOGOUT = 'explicit-logout';
const IDENTITY_CHANGED = 'identity-changed';
const MAX_SEEN_NONCES = 64;

type AuthBoundaryListener = () => void;
type IdentityChangeListener = (userId?: string) => void;
type AuthBoundaryMessageType = typeof EXPLICIT_LOGOUT | typeof IDENTITY_CHANGED;

type AuthBoundaryMessage = {
  type: AuthBoundaryMessageType;
  nonce?: string;
  user_id?: string;
};

const logoutListeners = new Set<AuthBoundaryListener>();
const identityListeners = new Set<IdentityChangeListener>();
const seenNonces = new Set<string>();
let authChannel: BroadcastChannel | null | undefined;
let storageListening = false;

function dispatch(message: unknown): void {
  const authMessage = message as Partial<AuthBoundaryMessage> | null;
  if (authMessage?.type !== EXPLICIT_LOGOUT && authMessage?.type !== IDENTITY_CHANGED) return;
  if (authMessage.nonce) {
    if (seenNonces.has(authMessage.nonce)) return;
    seenNonces.add(authMessage.nonce);
    if (seenNonces.size > MAX_SEEN_NONCES) {
      const oldest = seenNonces.values().next().value;
      if (oldest) seenNonces.delete(oldest);
    }
  }
  if (authMessage.type === EXPLICIT_LOGOUT) {
    logoutListeners.forEach((listener) => listener());
    return;
  }
  identityListeners.forEach((listener) => listener(authMessage.user_id));
}

function onStorage(event: StorageEvent): void {
  if (event.key !== AUTH_STORAGE_KEY || !event.newValue) return;
  try {
    dispatch(JSON.parse(event.newValue));
  } catch {
    // Ignore malformed values from unrelated scripts sharing the origin.
  }
}

function ensureTransport(): BroadcastChannel | null {
  if (authChannel === undefined) {
    if (typeof window === 'undefined' || typeof window.BroadcastChannel === 'undefined') {
      authChannel = null;
    } else {
      try {
        authChannel = new window.BroadcastChannel(AUTH_CHANNEL_NAME);
        authChannel.addEventListener('message', (event: MessageEvent<unknown>) => {
          dispatch(event.data);
        });
      } catch {
        // Some privacy modes expose BroadcastChannel but reject construction.
        authChannel = null;
      }
    }
  }
  if (!storageListening && typeof window !== 'undefined') {
    window.addEventListener('storage', onStorage);
    storageListening = true;
  }
  return authChannel;
}

function closeTransportIfUnused(): void {
  if (logoutListeners.size !== 0 || identityListeners.size !== 0) return;
  authChannel?.close();
  authChannel = undefined;
  seenNonces.clear();
  if (storageListening && typeof window !== 'undefined') {
    window.removeEventListener('storage', onStorage);
    storageListening = false;
  }
}

function subscribe(
  listeners: Set<AuthBoundaryListener>,
  listener: AuthBoundaryListener,
): () => void {
  listeners.add(listener);
  ensureTransport();
  return () => {
    listeners.delete(listener);
    closeTransportIfUnused();
  };
}

export function subscribeExplicitLogout(listener: AuthBoundaryListener): () => void {
  return subscribe(logoutListeners, listener);
}

export function subscribeIdentityChange(listener: IdentityChangeListener): () => void {
  identityListeners.add(listener);
  ensureTransport();
  return () => {
    identityListeners.delete(listener);
    closeTransportIfUnused();
  };
}

function broadcast(type: AuthBoundaryMessageType, userId?: string): void {
  const message: AuthBoundaryMessage = {
    type,
    nonce: `${Date.now()}-${Math.random()}`,
    user_id: userId,
  };
  const channel = ensureTransport();
  if (channel) {
    try {
      channel.postMessage(message);
    } catch {
      // Storage below remains an independent best-effort transport.
    }
  }
  if (typeof window === 'undefined') return;
  try {
    // The value is removed immediately: this is a storage-event transport for
    // active tabs, not a persistent marker that survives reload.
    window.localStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify(message));
    window.localStorage.removeItem(AUTH_STORAGE_KEY);
  } catch {
    // Single-tab auth remains correct when browser storage is unavailable.
  }
}

export function broadcastExplicitLogout(): void {
  broadcast(EXPLICIT_LOGOUT);
}

export function broadcastIdentityChange(userId: string): void {
  broadcast(IDENTITY_CHANGED, userId);
}
