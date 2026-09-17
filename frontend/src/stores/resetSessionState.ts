/**
 * resetSessionState — the ONE place that forgets a user (二次复审 P0-2).
 *
 * The workspace is a SPA, and every login path ends in an in-app
 * `navigate('/')`, not in a document load:
 *
 *   logout()                 → clearAuth() + navigate('/auth/login')
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
 * Register one store's "forget everything about the current user" action.
 *
 * Call it at module scope, next to the store definition — that is the only
 * place that knows which fields are session-scoped.
 */
export function registerSessionReset(fn: ResetFn): void {
  resetters.push(fn);
}

/**
 * Wipe every store whose contents belong to ONE identity.
 *
 * Deliberately NOT cleared: `useThemeStore` (a device preference, not user
 * data) and `useOrganizationStore` (its own lifecycle — it is keyed by the
 * organization, and the backend re-authorizes it per request).
 *
 * A failing resetter must not abort the others: a half-reset session is
 * strictly better than an exception thrown from inside a logout.
 */
export function resetSessionScopedState(): void {
  for (const reset of resetters) {
    try {
      reset();
    } catch {
      // Best-effort by design; see the note above.
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
