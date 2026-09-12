import { describe, expect, it } from 'vitest';
import {
  buildShortcutGroups,
  resolveDefaultApplication,
} from '../useApplicationCatalogStore';
import type { V2Application } from '@/services/runApi';

const app = (over: Partial<V2Application>): V2Application => ({
  id: 1, slug: 's', name: 'A', description: '', icon: '', kind: 'chat',
  runtime_type: 'agent', provider_key: 'feishu_aily', identity_mode: 'user',
  execution_mode: 'interactive', capabilities: {},
  ...over,
});

describe('buildShortcutGroups (§35 home shortcuts)', () => {
  it('splits favourites, frequent, recent and recommended', () => {
    const catalog = [
      app({ id: 1, name: '销售助手', is_favorite: true, usage_count: 1,
        last_used_at: '2026-09-09T00:00:00Z' }),
      app({ id: 2, name: '采购助手', usage_count: 9, last_used_at: '2026-09-10T00:00:00Z' }),
      app({ id: 3, name: '制度助手', usage_count: 4, last_used_at: '2026-09-11T00:00:00Z' }),
      app({ id: 4, name: '闲置助手' }),
    ];

    const groups = buildShortcutGroups(catalog);

    expect(groups.favorites.map((a) => a.id)).toEqual([1]);
    expect(groups.frequent.map((a) => a.id)).toEqual([2, 3, 1]);
    expect(groups.recent.map((a) => a.id)).toEqual([3, 2, 1]);
    // A published-but-never-used agent must stay reachable (§35 推荐).
    expect(groups.recommended.map((a) => a.id)).toEqual([4]);
  });

  it('recommends by global popularity for a brand-new account', () => {
    const catalog = [
      app({ id: 1, name: 'B', global_usage_count: 3 }),
      app({ id: 2, name: 'A', global_usage_count: 10 }),
    ];

    const groups = buildShortcutGroups(catalog);

    expect(groups.recommended.map((a) => a.id)).toEqual([2, 1]);
    expect(groups.recent).toEqual([]);
    expect(groups.frequent).toEqual([]);
    expect(groups.favorites).toEqual([]);
  });

  it('keeps used applications out of 推荐 (they may repeat across the others)', () => {
    const catalog = [
      app({ id: 1, name: 'A', is_favorite: true, usage_count: 2,
        last_used_at: '2026-09-10T00:00:00Z' }),
      app({ id: 2, name: 'B', usage_count: 5, last_used_at: '2026-09-11T00:00:00Z' }),
    ];

    const groups = buildShortcutGroups(catalog);

    // An app legitimately shows up in 收藏 / 常用 / 最近 at the same time…
    expect(groups.favorites.map((a) => a.id)).toEqual([1]);
    expect(groups.frequent.map((a) => a.id)).toEqual([2, 1]);
    expect(groups.recent.map((a) => a.id)).toEqual([2, 1]);
    // …but 推荐 only ever holds applications the user has not used yet.
    expect(groups.recommended).toEqual([]);
  });

  it('excludes non-chat applications from the agent shortcuts', () => {
    const catalog = [
      app({ id: 1, name: '销售助手' }),
      app({ id: 2, name: '修改OA密码', kind: 'custom', is_favorite: true,
        usage_count: 5, last_used_at: '2026-09-11T00:00:00Z' }),
    ];

    const groups = buildShortcutGroups(catalog);

    expect(groups.favorites).toEqual([]);
    expect(groups.frequent).toEqual([]);
    expect(groups.recent).toEqual([]);
    expect(groups.recommended.map((a) => a.id)).toEqual([1]);
  });

  it('caps each group at the requested limit', () => {
    const catalog = Array.from({ length: 12 }, (_, i) => app({
      id: i + 1, name: `app-${i}`, usage_count: 12 - i,
      last_used_at: `2026-09-${String(i + 1).padStart(2, '0')}T00:00:00Z`,
    }));

    const groups = buildShortcutGroups(catalog, 3);

    expect(groups.frequent).toHaveLength(3);
    expect(groups.recent).toHaveLength(3);
    expect(groups.recommended).toHaveLength(0);
  });

  it('handles an empty catalog', () => {
    expect(buildShortcutGroups([])).toEqual({
      favorites: [], frequent: [], recent: [], recommended: [],
    });
  });
});

describe('resolveDefaultApplication (§38 main agent)', () => {
  it('prefers the application explicitly marked as the main agent', () => {
    const catalog = [
      app({ id: 1, name: '创作助手' }),
      app({ id: 2, name: '销售助手', is_default_agent: true }),
    ];

    // Not the first row: the Admin-configured main agent wins.
    expect(resolveDefaultApplication(catalog)?.id).toBe(2);
  });

  it('falls back to the first bound chat application', () => {
    const catalog = [
      app({ id: 1, name: '创作助手' }),
      app({ id: 2, name: '销售助手' }),
    ];

    expect(resolveDefaultApplication(catalog)?.id).toBe(1);
  });

  it('never returns a fixed page or an unbound chat application', () => {
    const catalog = [
      app({ id: 1, name: '修改OA密码', kind: 'custom', is_default_agent: false }),
      app({ id: 2, name: '草稿助手', is_bound: false }),
      app({ id: 3, name: '创作助手', is_bound: true }),
    ];

    expect(resolveDefaultApplication(catalog)?.id).toBe(3);
  });

  it('returns undefined when nothing is runnable', () => {
    expect(resolveDefaultApplication([])).toBeUndefined();
    expect(resolveDefaultApplication([
      app({ id: 1, name: '修改OA密码', kind: 'custom' }),
    ])).toBeUndefined();
  });
});
