const AUTH_CHANNEL_NAME = 'studio-auth';
const AUTH_STORAGE_KEY = 'studio-auth-event';
const EXPLICIT_LOGOUT = 'explicit-logout';
const MAX_SEEN_NONCES = 64;

type ExplicitLogoutListener = () => void;

type AuthBoundaryMessage = {
  type: typeof EXPLICIT_LOGOUT;
  nonce?: string;
};

const listeners = new Set<ExplicitLogoutListener>();
const seenNonces = new Set<string>();
let authChannel: BroadcastChannel | null | undefined;
let storageListening = false;

function dispatch(message: unknown): void {
  const authMessage = message as Partial<AuthBoundaryMessage> | null;
  if (authMessage?.type !== EXPLICIT_LOGOUT) return;
  if (authMessage.nonce) {
    if (seenNonces.has(authMessage.nonce)) return;
    seenNonces.add(authMessage.nonce);
    if (seenNonces.size > MAX_SEEN_NONCES) {
      const oldest = seenNonces.values().next().value;
      if (oldest) seenNonces.delete(oldest);
    }
  }
  listeners.forEach((listener) => listener());
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
  if (listeners.size !== 0) return;
  authChannel?.close();
  authChannel = undefined;
  seenNonces.clear();
  if (storageListening && typeof window !== 'undefined') {
    window.removeEventListener('storage', onStorage);
    storageListening = false;
  }
}

export function subscribeExplicitLogout(listener: ExplicitLogoutListener): () => void {
  listeners.add(listener);
  ensureTransport();
  return () => {
    listeners.delete(listener);
    closeTransportIfUnused();
  };
}

export function broadcastExplicitLogout(): void {
  const message: AuthBoundaryMessage = {
    type: EXPLICIT_LOGOUT,
    nonce: `${Date.now()}-${Math.random()}`,
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
    // active tabs, not a persistent "logged out" marker that survives reload.
    window.localStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify(message));
    window.localStorage.removeItem(AUTH_STORAGE_KEY);
  } catch {
    // Single-tab logout remains correct when browser storage is unavailable.
  }
}
