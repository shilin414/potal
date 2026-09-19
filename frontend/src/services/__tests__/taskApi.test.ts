import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ get: vi.fn(), patch: vi.fn(), delete: vi.fn() }));
vi.mock('@/services/api', () => ({ api: mocks }));

import { deleteTask, fetchTasks, renameTask } from '@/services/taskApi';

const payload = {
  id: '7', title: '销售分析', application_id: 3, application_slug: 'sales',
  application_name: '销售助手', preview: '继续分析', preview_role: 'user',
  created_at: '2026-09-19T01:00:00Z', updated_at: '2026-09-19T02:00:00Z',
  execution_state: 'running' as const,
};

beforeEach(() => {
  mocks.get.mockReset().mockResolvedValue({ items: [payload], next_cursor: 'next' });
  mocks.patch.mockReset().mockResolvedValue(payload);
  mocks.delete.mockReset().mockResolvedValue(undefined);
});

describe('taskApi', () => {
  it('uses the Task facade and adapts snake_case payloads', async () => {
    const page = await fetchTasks({ limit: 20, q: ' 销售 ', applicationId: 3 });
    expect(mocks.get).toHaveBeenCalledWith('/v2/tasks', {
      cursor: undefined, limit: 20, q: '销售', application_id: 3,
    });
    expect(page.items[0]).toMatchObject({ id: '7', applicationId: 3, applicationSlug: 'sales', executionState: 'running' });
  });

  it('renames and deletes through product-facing task routes', async () => {
    await renameTask('7', '新标题');
    expect(mocks.patch).toHaveBeenCalledWith('/v2/tasks/7', { title: '新标题' });
    await deleteTask('7');
    expect(mocks.delete).toHaveBeenCalledWith('/v2/tasks/7');
  });
});
