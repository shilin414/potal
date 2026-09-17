import { describe, expect, it } from 'vitest';
import {
  MOBILE_RECENT_LIMIT,
  MOBILE_SHEET_MAX_ROWS,
  UNCATEGORIZED_SLUG,
  buildMobileCategories,
  buildMobileCategoryTabs,
  buildRecentItems,
  filterMobileCatalog,
  mergeRecentItems,
  splitMobileCatalog,
} from '../mobileCatalog';
import type { ApplicationSummary, V2Application } from '@/services/runApi';

const app = (over: Partial<V2Application>): V2Application => ({
  id: 1, slug: 's', name: 'A', description: '', icon: '', kind: 'chat',
  runtime_type: 'agent', provider_key: 'feishu_aily', capabilities: {},
  ...over,
});

const summary = (over: Partial<ApplicationSummary> & { id: number }): ApplicationSummary => ({
  slug: `s-${over.id}`, name: `A${over.id}`, description: '', icon: '',
  kind: 'chat', enabled: true,
  ...over,
});

// 执行报告 §4.2/§6 (P0-R1): in server-paged mode 最近使用 is merged from the
// shell's local recency and the server's recency group — NOT from the fetched
// page — so the section survives both paging and a category switch.
describe('mergeRecentItems (server-paged mode)', () => {
  it('leads with local recency, then the server group', () => {
    const local = [summary({ id: 1 }), summary({ id: 2 })];
    const server = [summary({ id: 2 }), summary({ id: 3 })];

    const merged = mergeRecentItems(local, server, 'agent');

    expect(merged.map((item) => item.id)).toEqual([1, 2, 3]);
  });

  it('keeps agents and applications apart', () => {
    const local = [summary({ id: 1 }), summary({ id: 2, kind: 'custom' })];

    expect(mergeRecentItems(local, [], 'agent').map((i) => i.id)).toEqual([1]);
    expect(mergeRecentItems(local, [], 'app').map((i) => i.id)).toEqual([2]);
  });

  it('never offers a disabled application, and caps the list', () => {
    const rows = [
      ...Array.from({ length: MOBILE_RECENT_LIMIT + 3 }, (_, i) => summary({ id: i + 1 })),
      summary({ id: 99, enabled: false }),
    ];

    const merged = mergeRecentItems(rows, [], 'agent');

    expect(merged).toHaveLength(MOBILE_RECENT_LIMIT);
    expect(merged.some((item) => item.id === 99)).toBe(false);
  });

  it('is independent of any category filter', () => {
    // Nothing here knows about categories: an HR-filtered list must still be
    // able to show an IT agent in 最近使用 (§4.2).
    const it = summary({ id: 7, category_slug: 'it', category_name: 'IT运维' });
    const hr = summary({ id: 8, category_slug: 'hr', category_name: 'HR' });

    expect(mergeRecentItems([it], [hr], 'agent').map((i) => i.id)).toEqual([7, 8]);
  });
});

describe('large catalogs (the shared DB carries 1800+ rows)', () => {
  const bulk = (n: number, over: (i: number) => Partial<V2Application> = () => ({})) =>
    Array.from({ length: n }, (_, i) => app({ id: i + 1, slug: `a-${i}`, name: `A${i}`, ...over(i) }));

  it('keeps search and 最近使用 working past the render cap', () => {
    // The cap is a RENDER budget, so the two lookups that are allowed to see
    // the whole catalog must still do so. Row count is asserted in the
    // component test; here we pin the data contract they rely on.
    const catalog = bulk(MOBILE_SHEET_MAX_ROWS + 10, (i) => (
      i === MOBILE_SHEET_MAX_ROWS + 5 ? { last_used_at: '2026-09-16T00:00:00Z' } : {}));

    // A recent id resolves even though its row is far past the cap…
    const recent = buildRecentItems(catalog, [MOBILE_SHEET_MAX_ROWS + 6], 'agent');
    expect(recent.map((a) => a.id)).toEqual([MOBILE_SHEET_MAX_ROWS + 6]);
    // …and search finds it too, because filtering reads the full pool.
    const found = filterMobileCatalog(catalog, 'all', `A${MOBILE_SHEET_MAX_ROWS + 5}`);
    expect(found.map((a) => a.id)).toEqual([MOBILE_SHEET_MAX_ROWS + 6]);
  });

  it('caps 最近使用 at MOBILE_RECENT_LIMIT regardless of catalog size', () => {
    const catalog = bulk(500, () => ({ last_used_at: '2026-09-16T00:00:00Z' }));
    expect(buildRecentItems(catalog, [], 'agent')).toHaveLength(MOBILE_RECENT_LIMIT);
  });
});

describe('splitMobileCatalog (§16.1/§16.2)', () => {
  it('separates chat agents from non-chat applications', () => {
    const { agents, apps } = splitMobileCatalog([
      app({ id: 1, kind: 'chat', name: '问数小安' }),
      app({ id: 2, kind: 'task', name: '定时报表' }),
      app({ id: 3, kind: 'custom', name: '条码流向查询' }),
      app({ id: 4, kind: 'chat', name: '运维小安' }),
    ]);

    expect(agents.map((a) => a.id)).toEqual([1, 4]);
    expect(apps.map((a) => a.id)).toEqual([2, 3]);
  });

  it('never lets a disabled application into a mobile surface', () => {
    const { agents, apps } = splitMobileCatalog([
      app({ id: 1, kind: 'chat', enabled: false }),
      app({ id: 2, kind: 'chat' }),
      app({ id: 3, kind: 'task', enabled: false }),
    ]);

    expect(agents.map((a) => a.id)).toEqual([2]);
    expect(apps).toEqual([]);
  });

  it('treats an absent enabled flag as enabled', () => {
    const { agents } = splitMobileCatalog([app({ id: 1, enabled: undefined })]);
    expect(agents.map((a) => a.id)).toEqual([1]);
  });
});

describe('buildRecentItems (§16.3)', () => {
  const catalog = [
    app({ id: 1, kind: 'chat', name: 'A' }),
    app({ id: 2, kind: 'chat', name: 'B' }),
    app({ id: 3, kind: 'chat', name: 'C' }),
    app({ id: 4, kind: 'chat', name: 'D' }),
    app({ id: 10, kind: 'task', name: '固定应用' }),
  ];

  it('ranks local recency above the server timestamp', () => {
    const items = buildRecentItems(catalog, [2, 1], 'agent');
    expect(items.map((a) => a.id)).toEqual([2, 1]);
  });

  it('backfills from last_used_at, newest first', () => {
    const withUsage = catalog.map((a, i) => (
      i < 3 ? { ...a, last_used_at: `2026-09-1${i + 1}T00:00:00Z` } : a));
    const items = buildRecentItems(withUsage, [1], 'agent');
    expect(items.map((a) => a.id)).toEqual([1, 3, 2]);
  });

  it('deduplicates an id present in both sources', () => {
    const withUsage = catalog.map((a) => (
      a.id === 1 ? { ...a, last_used_at: '2026-09-15T00:00:00Z' } : a));
    const items = buildRecentItems(withUsage, [1], 'agent');
    expect(items.map((a) => a.id)).toEqual([1]);
  });

  it('ignores recent ids that are not in the catalog', () => {
    const items = buildRecentItems(catalog, [999, 2], 'agent');
    expect(items.map((a) => a.id)).toEqual([2]);
  });

  it('isolates agents from applications — they do not share the 3 slots', () => {
    const mixed = buildRecentItems(
      catalog.map((a) => ({ ...a, last_used_at: '2026-09-15T00:00:00Z' })),
      [10, 1, 2, 3, 4],
      'agent',
    );
    expect(mixed.map((a) => a.id)).toEqual([1, 2, 3]);
    expect(mixed.some((a) => a.id === 10)).toBe(false);

    const apps = buildRecentItems(
      catalog.map((a) => ({ ...a, last_used_at: '2026-09-15T00:00:00Z' })),
      [10, 1, 2, 3, 4],
      'app',
    );
    expect(apps.map((a) => a.id)).toEqual([10]);
  });

  it(`caps at MOBILE_RECENT_LIMIT (${MOBILE_RECENT_LIMIT}), not the desktop 8`, () => {
    const many = Array.from({ length: 9 }, (_, i) => app({
      id: i + 1, kind: 'chat', last_used_at: `2026-09-0${(i % 9) + 1}T00:00:00Z`,
    }));
    expect(buildRecentItems(many, [], 'agent')).toHaveLength(3);
    expect(buildRecentItems(many, [], 'agent', 5)).toHaveLength(5);
    expect(buildRecentItems(many, [], 'agent', 0)).toEqual([]);
  });
});

describe('buildMobileCategories (§5.3)', () => {
  it('deduplicates, keeps first-appearance order and names the category', () => {
    const tabs = buildMobileCategories([
      app({ id: 1, category_slug: 'data', category_name: '数据分析' }),
      app({ id: 2, category_slug: 'it', category_name: 'IT运维' }),
      app({ id: 3, category_slug: 'data', category_name: '数据分析' }),
      app({ id: 4, category_slug: 'hr', category_name: 'HR' }),
    ]);

    expect(tabs).toEqual([
      { slug: 'data', name: '数据分析' },
      { slug: 'it', name: 'IT运维' },
      { slug: 'hr', name: 'HR' },
    ]);
  });

  it('creates no tab for an empty category, and no 全部 (the UI pins it)', () => {
    const tabs = buildMobileCategories([
      app({ id: 1, category_slug: '', category_name: '' }),
      app({ id: 2, category_slug: '   ', category_name: '  ' }),
      app({ id: 3, category_slug: 'data', category_name: '数据分析' }),
    ]);

    expect(tabs.map((t) => t.slug)).toEqual(['data']);
    expect(tabs.some((t) => t.slug === 'all')).toBe(false);
  });

  it('falls back to the slug when a category has no display name', () => {
    expect(buildMobileCategories([app({ id: 1, category_slug: 'ops' })]))
      .toEqual([{ slug: 'ops', name: 'ops' }]);
  });

  it('offers 其他 only when uncategorised rows actually exist', () => {
    expect(buildMobileCategoryTabs([
      app({ id: 1, category_slug: 'data', category_name: '数据分析' }),
    ]).map((t) => t.slug)).toEqual(['data']);

    expect(buildMobileCategoryTabs([
      app({ id: 1, category_slug: 'data', category_name: '数据分析' }),
      app({ id: 2, category_slug: '', category_name: '' }),
    ]).map((t) => t.slug)).toEqual(['data', UNCATEGORIZED_SLUG]);
  });
});

describe('filterMobileCatalog (§5.3/§5.4)', () => {
  const catalog = [
    app({ id: 1, name: '问数小安', description: '数据问答助手',
      category_slug: 'data', category_name: '数据分析' }),
    app({ id: 2, name: '运维小安', description: 'IT 运维助手',
      category_slug: 'it', category_name: 'IT运维' }),
    app({ id: 3, name: '合同助手', description: '合同分析助手' }),
  ];

  it('全部 passes everything through', () => {
    expect(filterMobileCatalog(catalog, 'all', '').map((a) => a.id))
      .toEqual([1, 2, 3]);
  });

  it('filters by category slug', () => {
    expect(filterMobileCatalog(catalog, 'data', '').map((a) => a.id)).toEqual([1]);
  });

  it('buckets uncategorised rows under 其他', () => {
    expect(filterMobileCatalog(catalog, UNCATEGORIZED_SLUG, '').map((a) => a.id))
      .toEqual([3]);
  });

  it('matches name, description and category name, case-insensitively', () => {
    expect(filterMobileCatalog(catalog, 'all', '运维').map((a) => a.id)).toEqual([2]);
    expect(filterMobileCatalog(catalog, 'all', 'it').map((a) => a.id)).toEqual([2]);
    expect(filterMobileCatalog(catalog, 'all', 'IT').map((a) => a.id)).toEqual([2]);
    expect(filterMobileCatalog(catalog, 'all', '助手').map((a) => a.id))
      .toEqual([1, 2, 3]);
  });

  it('returns NOTHING for an unmatched query (never falls back to everything)', () => {
    expect(filterMobileCatalog(catalog, 'all', 'zzz')).toEqual([]);
  });

  it('ANDs the category with the query', () => {
    expect(filterMobileCatalog(catalog, 'it', '数据')).toEqual([]);
    expect(filterMobileCatalog(catalog, 'data', '数据').map((a) => a.id)).toEqual([1]);
  });

  it('ignores surrounding whitespace in the query', () => {
    expect(filterMobileCatalog(catalog, 'all', '  运维  ').map((a) => a.id))
      .toEqual([2]);
  });
});
