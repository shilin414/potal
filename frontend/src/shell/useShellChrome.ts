import { useMemo } from 'react';
import { useMatches } from 'react-router-dom';

/**
 * Per-route shell decoration. Routes declare it via `handle.shell`, and the
 * AppShell merges every matched level — so console pages can go fullscreen
 * without duplicating a layout component, and without unmounting the shell
 * (architecture doc §33: AppShell 始终不卸载).
 */
export interface ShellChrome {
  hideHeader: boolean;
  hideSidebar: boolean;
  /** Console pages scroll and pad; workspaces manage their own scrolling. */
  padded: boolean;
}

export const DEFAULT_CHROME: ShellChrome = {
  hideHeader: false,
  hideSidebar: false,
  padded: false,
};

export function mergeChrome(
  chrome: ShellChrome,
  patch?: Partial<ShellChrome>,
): ShellChrome {
  return patch ? { ...chrome, ...patch } : chrome;
}

export function useShellChrome(): ShellChrome {
  const matches = useMatches();
  return useMemo(() => matches.reduce<ShellChrome>(
    (acc, match) => mergeChrome(acc, (match.handle as any)?.shell),
    DEFAULT_CHROME,
  ), [matches]);
}
