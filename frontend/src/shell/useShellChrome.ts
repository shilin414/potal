import { useMemo } from 'react';
import { useMatches } from 'react-router-dom';

export type MobileHeaderMode = 'workspace' | 'page' | 'detail' | 'console';
export type MobileHeaderActionType = 'new-task' | 'create' | 'search' | 'more' | 'none';

export interface MobileShellChrome {
  mode?: MobileHeaderMode;
  title?: string;
  showMenu?: boolean;
  showBack?: boolean;
  backTo?: string;
  action?: MobileHeaderActionType;
}

/**
 * Per-route shell decoration. Routes declare it via `handle.shell`, and the
 * AppShell merges every matched level — so console pages can go fullscreen
 * without duplicating a layout component, and without unmounting the shell.
 */
export interface ShellChrome {
  hideHeader: boolean;
  hideSidebar: boolean;
  /** Console pages scroll and pad; workspaces manage their own scrolling. */
  padded: boolean;
  mobile?: MobileShellChrome;
}

export const DEFAULT_CHROME: ShellChrome = {
  hideHeader: false,
  hideSidebar: false,
  padded: false,
  mobile: { mode: 'workspace', action: 'new-task' },
};

export function mergeChrome(
  chrome: ShellChrome,
  patch?: Partial<ShellChrome>,
): ShellChrome {
  if (!patch) return chrome;
  return {
    ...chrome,
    ...patch,
    mobile: patch.mobile ? { ...chrome.mobile, ...patch.mobile } : chrome.mobile,
  };
}

export function useShellChrome(): ShellChrome {
  const matches = useMatches();
  return useMemo(() => matches.reduce<ShellChrome>(
    (acc, match) => mergeChrome(acc, (match.handle as any)?.shell),
    DEFAULT_CHROME,
  ), [matches]);
}
