/**
 * Contract tests for the 智能体市场 authoring client.
 *
 * These pin the request shape (URL / verb / params / body) the backend expects,
 * which is exactly what silently breaks when either side is refactored. No
 * network: `@/services/api` is replaced by spies.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  patch: vi.fn(),
  delete: vi.fn(),
}));

vi.mock('@/services/api', () => ({
  api: {
    get: mocks.get,
    post: mocks.post,
    patch: mocks.patch,
    delete: mocks.delete,
  },
}));

import {
  clearAgentAvatar,
  createAgentApplication,
  deleteAgentApplication,
  fetchAgentRuntimes,
  fetchWorkspaceBootstrap,
  resolveApplication,
  resolveApplicationMention,
  setDefaultAgent,
  updateAgentApplication,
  uploadAgentAvatar,
  validateAgentRuntime,
} from '@/services/runApi';

beforeEach(() => {
  mocks.get.mockReset().mockResolvedValue([]);
  mocks.post.mockReset().mockResolvedValue({});
  mocks.patch.mockReset().mockResolvedValue({});
  mocks.delete.mockReset().mockResolvedValue({});
});

// 执行报告 §9–§14 (P1-1/P1-2). The shell's start-up data and its single-row
// lookups are what replaced the whole-catalog download, so the CONTRACT of
// those three calls is worth pinning: a wrong URL or a dropped parameter shows
// up as "the shell is empty" rather than as an obvious error.
describe('workspace bootstrap + single-application resolution', () => {
  it('reads the constant-size start-up payload', async () => {
    await fetchWorkspaceBootstrap();

    expect(mocks.get).toHaveBeenCalledWith('/v2/workspace/bootstrap');
  });

  it('never asks for the legacy whole-catalog array any more', async () => {
    // A regression here is invisible in a small dev DB and fatal in a large
    // one, so it is asserted rather than trusted: the module must not even
    // export the legacy helpers.
    const runApi = await import('@/services/runApi');
    expect('fetchV2Applications' in runApi).toBe(false);
    expect('fetchManageableAgents' in runApi).toBe(false);
  });

  it('resolves one application by slug or id', async () => {
    await resolveApplication({ slug: 'sales' });
    expect(mocks.get).toHaveBeenCalledWith(
      '/v2/applications/resolve', { slug: 'sales' });

    await resolveApplication({ id: 7 });
    expect(mocks.get).toHaveBeenLastCalledWith(
      '/v2/applications/resolve', { id: '7' });
  });

  it('resolves @mention candidates server-side', async () => {
    await resolveApplicationMention('  销售助手 ');

    expect(mocks.get).toHaveBeenCalledWith(
      '/v2/applications/resolve-mention', { q: '销售助手' });
  });

  it('treats an unreachable mention resolver as "no candidates"', async () => {
    mocks.get.mockRejectedValueOnce(new Error('offline'));

    // Routing is a convenience: an unreachable resolver must degrade to
    // "unknown mention → send as typed", never to a blocked composer (§38).
    await expect(resolveApplicationMention('销售助手')).resolves.toEqual([]);
  });

  it('does not call the API for an empty mention token', async () => {
    await expect(resolveApplicationMention('   ')).resolves.toEqual([]);
    expect(mocks.get).not.toHaveBeenCalled();
  });
});

describe('智能体市场 authoring', () => {
  it('reads the runtime catalog for the create form', async () => {
    await fetchAgentRuntimes();

    expect(mocks.get).toHaveBeenCalledWith('/v2/runtimes');
  });

  it('creates an application with its runtime binding', async () => {
    const payload = {
      name: '销售助手',
      slug: 'sales-assistant',
      runtime: {
        provider_key: 'feishu_aily',
        runtime_type: 'agent',
        external_resource_id: 'agent_4jz9pu9exyaws',
      },
    };

    await createAgentApplication(payload);

    expect(mocks.post).toHaveBeenCalledWith('/v2/applications', payload);
  });

  it('patches an existing agent in place', async () => {
    await updateAgentApplication(7, { name: '销售助手 V2' });

    expect(mocks.patch).toHaveBeenCalledWith(
      '/v2/applications/7', { name: '销售助手 V2' });
  });

  it('deletes an agent by id', async () => {
    await deleteAgentApplication(7);

    expect(mocks.delete).toHaveBeenCalledWith('/v2/applications/7');
  });

  it('uploads the avatar as multipart form data', async () => {
    const file = new File([new Uint8Array([1, 2, 3])], 'a.png', {
      type: 'image/png',
    });

    await uploadAgentAvatar(7, file);

    const [url, body] = mocks.post.mock.calls[0];
    expect(url).toBe('/v2/applications/7/avatar');
    expect(body).toBeInstanceOf(FormData);
    expect((body as FormData).get('file')).toBe(file);
  });

  it('clears the avatar', async () => {
    await clearAgentAvatar(7);

    expect(mocks.delete).toHaveBeenCalledWith('/v2/applications/7/avatar');
  });

  it('promotes and demotes the workspace main agent', async () => {
    await setDefaultAgent(7, true);
    expect(mocks.post).toHaveBeenCalledWith('/v2/applications/7/default-agent');

    await setDefaultAgent(7, false);
    expect(mocks.delete).toHaveBeenCalledWith('/v2/applications/7/default-agent');
  });

  it('validates a runtime resource before saving', async () => {
    const payload = {
      provider_key: 'feishu_aily',
      runtime_type: 'agent',
      external_resource_id: 'agent_x',
    };

    await validateAgentRuntime(payload);

    expect(mocks.post).toHaveBeenCalledWith('/v2/runtimes/validate', payload);
  });
});
