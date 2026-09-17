/**
 * URL contract tests for the catalog client (二次复审 P0-1).
 *
 * Why this file exists: `fetchWorkspaceBootstrap()` used to call
 * `'/workspace/bootstrap'`. Combined with the axios instance's
 * `baseURL = '/api'` that produced `GET /api/workspace/bootstrap`, while the
 * server registers — and ONLY registers — the OpenAPI path
 * `GET /api/v2/workspace/bootstrap` (`genapi.HandlerFromMux`, no alias). The
 * bug was invisible to every existing test because the store suites mock
 * `@/services/api` away entirely: they assert "the service was called", never
 * "which URL it called".
 *
 * So this suite mocks one level LOWER (the axios instance) and asserts the
 * real path string. The `/v2` prefix is the load-bearing part: every studio
 * catalog endpoint lives under it, and a missing prefix is a silent 404
 * rather than a type error.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

const axiosMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  patch: vi.fn(),
  delete: vi.fn(),
}));

vi.mock('@/services/axios', () => ({ default: axiosMock }));

import {
  fetchApplicationPage,
  fetchWorkspaceBootstrap,
  resolveApplication,
  resolveApplicationMention,
} from '@/services/runApi';

beforeEach(() => {
  axiosMock.get.mockReset().mockResolvedValue({ items: [], next_cursor: '', has_more: false });
  axiosMock.post.mockReset().mockResolvedValue({});
  axiosMock.patch.mockReset().mockResolvedValue({});
  axiosMock.delete.mockReset().mockResolvedValue({});
});

/** The path the axios instance actually received (params are second). */
const requestedPath = (call: number = 0): string => axiosMock.get.mock.calls[call][0];

describe('catalog client URL contract', () => {
  it('reads the workspace bootstrap from the OpenAPI path', async () => {
    await fetchWorkspaceBootstrap();

    // The regression: this used to be '/workspace/bootstrap'.
    expect(requestedPath()).toBe('/v2/workspace/bootstrap');
  });

  it('resolves a single application under /v2', async () => {
    await resolveApplication({ slug: 'sales' });

    expect(requestedPath()).toBe('/v2/applications/resolve');
    expect(axiosMock.get.mock.calls[0][1]).toEqual({ params: { slug: 'sales' } });
  });

  it('resolves an `@` mention under /v2', async () => {
    await resolveApplicationMention('销售助手');

    expect(requestedPath()).toBe('/v2/applications/resolve-mention');
  });

  it('reads a keyset page under /v2', async () => {
    await fetchApplicationPage({ kind: 'chat', limit: 24 });

    expect(requestedPath()).toBe('/v2/applications/page');
  });

  it('keeps EVERY catalog path under the /v2 namespace', async () => {
    // The invariant behind P0-1: the server mounts only generated OpenAPI
    // routes, so a path outside /v2 is a guaranteed 404. This is asserted
    // over the real calls rather than over one hand-picked string, so a new
    // endpoint added without the prefix fails here.
    await fetchWorkspaceBootstrap();
    await resolveApplication({ id: 7 });
    await resolveApplicationMention('x');
    await fetchApplicationPage({ kind: 'fixed' });

    const paths = axiosMock.get.mock.calls.map((call) => call[0] as string);
    expect(paths).toHaveLength(4);
    for (const path of paths) {
      expect(path.startsWith('/v2/')).toBe(true);
    }
  });

  it('does not send defaults the backend already applies', async () => {
    await fetchApplicationPage({ kind: 'chat', scope: 'public', limit: 24 });

    // kind=chat / scope=public ARE the server defaults and are omitted;
    // `limit` is only sent when the caller actually chose one (otherwise the
    // server's own default applies and the URL stays identical).
    expect(axiosMock.get.mock.calls[0][1].params).toEqual({ limit: '24' });
  });

  it('does not send a page size the caller did not choose', async () => {
    await fetchApplicationPage({ kind: 'chat' });

    expect(axiosMock.get.mock.calls[0][1].params).toEqual({});
  });

  it('forwards the consume mode a consumer surface asks for', async () => {
    await fetchApplicationPage({ kind: 'chat', mode: 'consume', limit: 24 });

    expect(axiosMock.get.mock.calls[0][1].params).toEqual({
      limit: '24',
      mode: 'consume',
    });
  });

  it('omits mode on a management page, which stays on the default', async () => {
    // 智能体市场 / 应用中心 must keep seeing disabled / private / unbound
    // rows, so they must NOT ask for consume (P0-5).
    await fetchApplicationPage({ kind: 'chat', mode: 'manage', limit: 24 });

    expect(axiosMock.get.mock.calls[0][1].params).toEqual({ limit: '24' });
  });
});
