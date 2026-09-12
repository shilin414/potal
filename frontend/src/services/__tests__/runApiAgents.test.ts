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
  fetchManageableAgents,
  fetchV2Applications,
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

describe('fetchV2Applications (workspace catalog)', () => {
  it('keeps the legacy chat listing shape by default', async () => {
    await fetchV2Applications();

    expect(mocks.get).toHaveBeenCalledWith('/v2/applications', undefined);
  });

  it('passes kind, scope and include_unbound through', async () => {
    await fetchV2Applications('all', { scope: 'manage' });
    expect(mocks.get).toHaveBeenLastCalledWith(
      '/v2/applications', { kind: 'all', scope: 'manage' });

    await fetchV2Applications('chat', { scope: 'manage', includeUnbound: true });
    expect(mocks.get).toHaveBeenLastCalledWith(
      '/v2/applications', { scope: 'manage', include_unbound: 'true' });
  });

  it('degrades to an empty catalog instead of throwing', async () => {
    mocks.get.mockRejectedValueOnce(new Error('offline'));

    await expect(fetchV2Applications()).resolves.toEqual([]);
  });
});

describe('智能体市场 authoring', () => {
  it('lists manageable agents including unbound ones', async () => {
    await fetchManageableAgents();

    expect(mocks.get).toHaveBeenCalledWith('/v2/applications', {
      kind: 'chat', scope: 'manage', include_unbound: 'true',
    });
  });

  it('returns an empty list when the marketplace listing fails', async () => {
    mocks.get.mockRejectedValueOnce(new Error('boom'));

    await expect(fetchManageableAgents()).resolves.toEqual([]);
  });

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
