/**
 * mobileCatalog — pure derivations the mobile catalog surfaces share.
 *
 * The mobile 智能体/应用 selectors are Bottom Sheets over the catalog. Their
 * rows may come from either source, and both are accepted here because the
 * derivations only read display fields:
 *
 *   · the PAGED endpoint (`GET /api/v2/applications/page` → full items) in
 *     production, one page at a time;
 *   · the workspace bootstrap's summaries for 最近使用 and the category rails
 *     (执行报告 §11), which is what makes those independent of the page.
 *
 * Everything here is a pure projection — no React, no requests — which is why
 * it is unit-testable without a backend and why the two shells can never
 * disagree about what exists.
 *
 * Deliberately NOT reused from the desktop home:
 *   - `RECENT_LIMIT = 8` (HomeShortcuts) — a Bottom Sheet's top area must stay
 *     short, so mobile caps 最近使用 at 3 (design report §5.2/§6.2);
 *   - desktop 快捷分组 (收藏/常用/推荐) are a browsing taxonomy computed by the
 *     workspace bootstrap; mobile is a "pick one now" list (§2).
 */
import type { ApplicationSummary } from '@/services/runApi';

/** How many items each mobile selector shows under 最近使用 (§5.2). */
export const MOBILE_RECENT_LIMIT = 3;

/**
 * The SERVER page size the pickers request (执行报告 §25, 2026-09-17).
 *
 * Before the paged endpoint this constant was a RENDER budget over the whole
 * catalog (1800+ rows downloaded, first 60 rendered). Now the sheet asks
 * `GET /v2/applications/page?limit=60` and 加载更多 fetches the next cursor
 * page — the browser only ever holds what the user actually scrolled to.
 * `MOBILE_SHEET_MAX_ROWS` keeps its name and value for the legacy local-pool
 * mode (unit tests project a fully-injected catalog and still need the cap).
 */
export const MOBILE_SHEET_PAGE_SIZE = 60;

/**
 * How many rows the LEGACY local-pool mode renders before 加载更多.
 *
 * Paged mode no longer downloads a whole catalog, so this only guards the
 * test/fallback path where a complete pool is handed to the sheet.
 */
export const MOBILE_SHEET_MAX_ROWS = 60;

/** 智能体 = chat kind; everything else is an 应用 (§16.1/§16.2). */
export function isAgentApplication(application: ApplicationSummary): boolean {
  return application.kind === 'chat';
}

/**
 * Ids of the recently used applications, in the order the caller ranked them.
 *
 * NOT read out of `applications`: the server prunes nothing, and an id in the
 * shell's own navigation log may point at an application the current payload
 * does not carry (deleted, or a private one the caller can no longer see).
 * Callers must therefore treat a missing id as "skip it" rather than as an
 * error — but computing the list once here keeps it off the render path, where
 * it would run for each of the (few) rows that get rendered.
 */
export function recentIdsOf(recentIds: number[]): number[] {
  return recentIds.slice();
}

/**
 * The catalog split mobile navigates by (§16). Disabled applications are
 * dropped here rather than at each call site: mobile surfaces are consumption
 * surfaces, so a 停用 app must never be pickable — the same backstop
 * HomeShortcuts applies on desktop (a staff catalog can still contain them).
 */
export function splitMobileCatalog(applications: ApplicationSummary[]): {
  agents: ApplicationSummary[];
  apps: ApplicationSummary[];
} {
  const enabled = applications.filter((app) => app.enabled !== false);
  return {
    agents: enabled.filter(isAgentApplication),
    apps: enabled.filter((app) => !isAgentApplication(app)),
  };
}

/** epoch ms of an application's last use, 0 when never used. */
function lastUsed(app: ApplicationSummary): number {
  return app.last_used_at ? Date.parse(app.last_used_at) || 0 : 0;
}

/**
 * 最近使用 for ONE selector in SERVER-PAGED mode (执行报告 §4.2/§6, P0-R1).
 *
 * The sheet used to build this from the rows it had downloaded, which — once
 * the list became server-paginated — meant "recency within the current page":
 * an agent the user had just used vanished from 最近使用 as soon as it was not
 * on page one, and picking a category filtered the section away entirely.
 *
 * The two sources are now both independent of the current page:
 *
 *   · `localRecency` — the shell's navigation log, resolved through the
 *     entity cache (the application was resolved when the user opened it);
 *   · `serverRecency` — the `recent` / `recent_fixed_apps` group of the
 *     workspace bootstrap.
 *
 * Local first (it reflects what the user just did in THIS session, including
 * applications the server has no usage row for yet), deduplicated, capped, and
 * NEVER filtered by the selected category — the report is explicit that
 * 最近使用 stays independent of the list below it.
 */
export function mergeRecentItems(
  localRecency: ApplicationSummary[],
  serverRecency: ApplicationSummary[],
  type: 'agent' | 'app',
  limit = MOBILE_RECENT_LIMIT,
): ApplicationSummary[] {
  if (limit <= 0) return [];
  const out: ApplicationSummary[] = [];
  const seen = new Set<number>();
  for (const app of [...localRecency, ...serverRecency]) {
    if (!app || seen.has(app.id) || app.enabled === false) continue;
    const isAgent = app.kind === 'chat';
    if (type === 'agent' ? !isAgent : isAgent) continue;
    seen.add(app.id);
    out.push(app);
    if (out.length >= limit) break;
  }
  return out;
}

/**
 * 最近使用 for ONE selector (§16.3).
 *
 * Agents and applications are computed SEPARATELY — they must not compete for
 * the same 3 slots, otherwise opening a few fixed apps would push every agent
 * out of the agent sheet.
 *
 * Order: local recency (`recentIds`, the shell's own navigation log) first,
 * then the server's `last_used_at`, deduplicated, capped. Local recency leads
 * because it reflects what the user just did in THIS session, including
 * applications the server has not recorded a usage row for yet.
 */
export function buildRecentItems(
  applications: ApplicationSummary[],
  recentIds: number[],
  type: 'agent' | 'app',
  limit = MOBILE_RECENT_LIMIT,
): ApplicationSummary[] {
  if (limit <= 0) return [];
  const pool = applications.filter((app) => (
    type === 'agent' ? isAgentApplication(app) : !isAgentApplication(app)));

  const byId = new Map<number, ApplicationSummary>();
  pool.forEach((app) => byId.set(app.id, app));

  const seen = new Set<number>();
  const out: ApplicationSummary[] = [];
  const push = (app: ApplicationSummary | undefined) => {
    if (!app || seen.has(app.id) || out.length >= limit) return;
    seen.add(app.id);
    out.push(app);
  };

  recentIds.forEach((id) => push(byId.get(id)));
  [...pool]
    .filter((app) => lastUsed(app) > 0)
    .sort((a, b) => lastUsed(b) - lastUsed(a))
    .forEach(push);

  return out;
}

export interface MobileCategory {
  slug: string;
  name: string;
}

/**
 * Category tabs for one selector (§5.3).
 *
 * 全部 is NOT produced here — the UI pins it as the first tab, because it is a
 * "no filter" affordance rather than a category the data owns.
 *
 * Order is first-appearance in the catalog, which matches the backend's
 * `ORDER BY created_at`: a stable, admin-controllable order that needs no new
 * sort column. Applications with no category are skipped entirely instead of
 * creating a meaningless tab.
 */
export function buildMobileCategories(
  applications: ApplicationSummary[],
): MobileCategory[] {
  const out: MobileCategory[] = [];
  const seen = new Set<string>();
  applications.forEach((app) => {
    const slug = (app.category_slug || '').trim();
    if (!slug || seen.has(slug)) return;
    seen.add(slug);
    out.push({ slug, name: (app.category_name || '').trim() || slug });
  });
  return out;
}

/** Tab key for applications the catalog does not categorise (§5.3). */
export const UNCATEGORIZED_SLUG = '__uncategorized__';

/**
 * Filter one selector's list by tab + search query (§5.3/§5.4).
 *
 * `categorySlug` is `'all'` for the pinned 全部 tab. Category and query are
 * ANDed, matching the report: picking a category then searching searches
 * within it.
 */
export function filterMobileCatalog(
  applications: ApplicationSummary[],
  categorySlug: string,
  query: string,
): ApplicationSummary[] {
  const q = query.trim().toLowerCase();
  return applications.filter((app) => {
    if (categorySlug !== 'all') {
      const slug = (app.category_slug || '').trim();
      if (categorySlug === UNCATEGORIZED_SLUG) {
        if (slug) return false;
      } else if (slug !== categorySlug) {
        return false;
      }
    }
    if (!q) return true;
    return [app.name, app.description, app.category_name]
      .some((value) => (value || '').toLowerCase().includes(q));
  });
}

/**
 * 分类 tabs plus the tab a selector should actually render.
 *
 * The report is explicit that a category with no rows must not appear, and
 * that the catch-all 其他 tab is only offered when uncategorised rows EXIST —
 * so both are decided against the rows the selector is about to show, not
 * against the raw catalog.
 */
export function buildMobileCategoryTabs(
  applications: ApplicationSummary[],
): MobileCategory[] {
  const tabs = buildMobileCategories(applications);
  const hasUncategorized = applications.some(
    (app) => !(app.category_slug || '').trim());
  if (hasUncategorized) {
    tabs.push({ slug: UNCATEGORIZED_SLUG, name: '其他' });
  }
  return tabs;
}
