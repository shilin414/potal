/** Normal session expiry re-enters the automatic Feishu OAuth route. */
export function navigateToSessionLogin(): void {
  window.location.href = '/login';
}
