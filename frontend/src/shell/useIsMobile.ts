import { useEffect, useState } from 'react';

/** Breakpoint below which the shell switches to the mobile layout (§20). */
export const MOBILE_MAX_WIDTH = 768;

const QUERY = `(max-width: ${MOBILE_MAX_WIDTH}px)`;

/**
 * Reactive viewport check. Guarded for non-DOM environments (vitest runs in
 * the node environment) so importing the shell never crashes tests.
 */
export function useIsMobile(query: string = QUERY): boolean {
  const [isMobile, setIsMobile] = useState(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return false;
    return window.matchMedia(query).matches;
  });

  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return undefined;
    const list = window.matchMedia(query);
    const onChange = (event: MediaQueryListEvent) => setIsMobile(event.matches);
    setIsMobile(list.matches);
    // Older Safari only supports addListener.
    if (list.addEventListener) {
      list.addEventListener('change', onChange);
      return () => list.removeEventListener('change', onChange);
    }
    list.addListener(onChange);
    return () => list.removeListener(onChange);
  }, [query]);

  return isMobile;
}
