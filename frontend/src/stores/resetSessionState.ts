/**
 * resetSessionState — the ONE place that forgets a user (二次复审 P0-2).
 *
 * The workspace is a SPA, and every login path ends in an in-app
 * `navigate('/')`, not in a document load:
 *
 *   explicit logout          → server revoke + reset + logged-out page
 *   login() / adminLogin()   → set({ user }) + navigate('/')
 *   completeSso()            → set({ user })
 *   completeFeishuLogin()    → useAuthStore.setState({ user })
 *
 * So the JS runtime survives a user switch — and with it every module-level
 * Zustand store that was never cleared. Concretely, user A's:
 *
 *   default agent, shortcut groups, resolved entities,
 *   composer drafts, per-application conversation ids, scroll offsets,
 *   skill selections, recent list, chat transcripts
 *
 * were still there when user B logged in. Two of those stores are even
 * `persist`ed (`workspace-storage`), so a real reload did not help either:
 * A's draft and conversation ids came back out of localStorage.
 *
 * The rule this module enforces:
 *
 *   EVERY path that ends one identity and begins another MUST call
 *   `resetSessionScopedState()` between the two.
 *
 * Session state is not limited to Zustand fields. A module-level Map, Set,
 * timer, AbortController, SSE handle or in-flight Promise registry can also
 * belong to the previous identity. Each registered resetter must clear those
 * side registries as well as returning its visible store fields to defaults.
 *
 * ── Why a REGISTRY and not an import list ────────────────────────────────
 *
 * The obvious shape is one file that imports every store and clears it. That
 * is a cycle: this module would import `useConversationStore`, which imports
 * `@/services/axios`, which imports `useAuthStore`, which imports this
 * module. Under Vitest that cycle silently broke `useRunChatStore`'s
 * dependency graph (its mocked `createRun` never ran) — a store test failing
 * for a reason that has nothing to do with the store.
 *
 * So the dependency is inverted: this module imports NOTHING, and each store
 * registers how to forget itself. A new store that forgets to register is a
 * new leak, but it can never break another module's imports.
 */
type ResetFn = () => void;

const resetters: ResetFn[] = [];

/**
 * The global session epoch (四次复审 P0-R1).
 *
 * This is the ONE generation every session-scoped async action captures
 * before it awaits. A login/logout that ends an identity bumps it, so an
 * answer that was already in flight for the previous user can never write
 * into the next user's state — the same rule the bootstrap and entity stores
 * already enforce locally, lifted to a single place so no store needs its
 * own copy.
 *
 * `let` + two tiny accessors keeps the epoch private to this module; no
 * caller can set it, and no caller can compare it except through the same
 * helper it captured from.
 */
let sessionGeneration = 0;

/** Snapshot the current epoch before an async operation starts. */
export function captureSessionGeneration(): number {
  return sessionGeneration;
}

/** `true` while `captured` still names the session that started the work. */
export function sessionStillCurrent(captured: number): boolean {
  return captured === sessionGeneration;
}

/**
 * Register one store's "forget everything about the current user" action.
 *
 * Call it at module scope, next to the store definition — that is the only
 * place that knows which fields are session-scoped.
 */
export function registerSessionReset(fn: ResetFn): void {
  resetters.push(fn);
}

/**
 * Persisted storage keys that belong to ONE identity (三次复审 P0-R2).
 *
 * The registry above only reaches stores whose module was actually IMPORTED.
 * A lazily-loaded store that never mounted during user A's session still has
 * A's rows in localStorage — and zustand's `persist` would hydrate them back
 * the first time user B's session loads that module. Wiping the KEYS here
 * closes that gap for every session-scoped persisted store, whether or not
 * its resetter is registered yet.
 *
 * Deliberately NOT in this list: `theme-storage` and any other real
 * cross-user client preference.
 *
 * `conversation-storage` is included ONLY as one-release legacy cleanup
 * (四次复审 P0-R2): the current store no longer persists transcripts, so no
 * new value is written, but an old browser may still hold one from a prior
 * build and it should not survive an identity switch on disk.
 */
const SESSION_SCOPED_STORAGE_KEYS = [
  'workspace-storage',
  'organization-storage',
  // Legacy cleanup only: the current store never writes this key.
  'conversation-storage',
] as const;

/**
 * Wipe every store whose contents belong to ONE identity — and the
 * localStorage keys that would hydrate them back.
 *
 * A failing resetter must not abort the others: a half-reset session is
 * strictly better than an exception thrown from inside a logout.
 */
export function resetSessionScopedState(): void {
  // Bump the epoch FIRST (四次复审 P0-R1): every request still travelling
  // when this identity ends must observe a stale generation the moment it
  // settles, including the `.finally` / timer / SSE callbacks that do not go
  // through a Store action.
  sessionGeneration += 1;

  for (const reset of resetters) {
    try {
      reset();
    } catch {
      // Best-effort by design; see the note above.
    }
  }
  for (const key of SESSION_SCOPED_STORAGE_KEYS) {
    try {
      localStorage.removeItem(key);
    } catch {
      // Storage can be unavailable (private mode); the in-memory reset above
      // still ran.
    }
  }
}

/**
 * `true` when both values name the same account.
 *
 * Compared as STRINGS: the identity snapshot's `id` arrives as a number from
 * the local-admin login and as a string from the Feishu exchange, so `===`
 * would declare the same user "different" and reset a session that never
 * changed (which would throw away the user's own composer draft and open
 * conversation on a harmless re-login).
 */
export function isSameUser(
  a: { id?: unknown } | null | undefined,
  b: { id?: unknown } | null | undefined,
): boolean {
  if (!a || !b) return false;
  const left = a.id == null ? '' : String(a.id);
  const right = b.id == null ? '' : String(b.id);
  return left !== '' && left === right;
}
