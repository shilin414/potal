/**
 * composerRouting — turning composer text into a workspace action, with the
 * `@mention` candidate list resolved by the SERVER (执行报告 §14).
 *
 * `mentionRouter` stays pure: it decides what to do with a candidate list.
 * This module is the thin IO shell around it, and it exists because the old
 * wiring built that candidate list from the whole catalog
 * (`applications.map(...)` in the home workspace and the chat renderer), so
 * `@` routing was the last place that forced a full catalog download.
 *
 * The interesting property: a mention resolves even for an application the
 * browser has never loaded. The token the user typed is enough — the server
 * answers with the few applications whose name/slug could match, ranked, and
 * the router picks among them exactly as it did before.
 */
import { parseMention, routeMention } from './mentionRouter';
import type { MentionCandidate, WorkspaceRoute } from './mentionRouter';
import { resolveApplicationMention } from '@/services/runApi';

export interface ComposerRoute {
  route: WorkspaceRoute;
  /** The application the decision refers to, when there is one. */
  target?: MentionCandidate;
}

/** The text after a LEADING `@`, up to the first whitespace. */
export function mentionToken(text: string): string {
  const trimmed = text.replace(/^\s+/, '');
  if (!trimmed.startsWith('@')) return '';
  return trimmed.slice(1).split(/\s/)[0].trim();
}

/**
 * Route one composer submission. Never throws and never blocks a send:
 *
 *   · no leading `@`                → passthrough, ZERO requests;
 *   · the resolver is unreachable   → passthrough with the text untouched
 *     (§38: an unknown mention must never surface an error);
 *   · a resolved mention            → the router's switch / open / send /
 *     ignore decision, carrying the resolved candidate so the caller can
 *     navigate without a second lookup.
 */
export async function routeComposerText(
  text: string,
  activeApplicationId: number | null,
): Promise<ComposerRoute> {
  const { mentioned } = parseMention(text, []);
  if (!mentioned) return { route: { action: 'passthrough' } };

  const token = mentionToken(text);
  let candidates: MentionCandidate[] = [];
  try {
    // Defence in depth: `resolveApplicationMention` already swallows its own
    // transport errors, but routing must stay total — a rejected promise here
    // would abort the SEND, which is a far worse outcome than an unrouted
    // mention (§38).
    if (token) candidates = await resolveApplicationMention(token);
  } catch {
    candidates = [];
  }
  const route = routeMention({ text, candidates, activeApplicationId });
  if (route.action !== 'switch' && route.action !== 'open') return { route };
  return { route, target: candidates.find((item) => item.id === route.applicationId) };
}
