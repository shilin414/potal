/**
 * scheduleApi 请求契约测试：钉死 URL / 动词 / 参数 / body。
 * 无网络：@/services/api 被 spy 替代。
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
  createSchedule,
  deleteSchedule,
  disableSchedule,
  enableSchedule,
  fetchSchedule,
  fetchScheduleOccurrences,
  fetchSchedules,
  previewScheduleRuns,
  runScheduleNow,
  updateSchedule,
} from '@/services/scheduleApi';

beforeEach(() => {
  mocks.get.mockReset().mockResolvedValue([]);
  mocks.post.mockReset().mockResolvedValue({});
  mocks.patch.mockReset().mockResolvedValue({});
  mocks.delete.mockReset().mockResolvedValue({});
});

describe('fetchSchedules', () => {
  it('always sends the status filter for a consistent contract', async () => {
    await fetchSchedules('all');
    expect(mocks.get).toHaveBeenCalledWith('/v2/schedules', { limit: 50, status: 'all' });
  });

  it('passes paused filter, keyset cursor and limit', async () => {
    await fetchSchedules('paused', 42, 20);
    expect(mocks.get).toHaveBeenCalledWith('/v2/schedules', {
      limit: 20,
      status: 'paused',
      before_id: 42,
    });
  });
});

describe('CRUD verbs', () => {
  it('posts create payloads to /v2/schedules', async () => {
    const payload = { name: '日报', application_id: 1, prompt: 'p', schedule_type: 'daily' as const };
    await createSchedule(payload);
    expect(mocks.post).toHaveBeenCalledWith('/v2/schedules', payload);
  });

  it('patches updates and deletes by id', async () => {
    await updateSchedule(7, { name: '新名' });
    expect(mocks.patch).toHaveBeenCalledWith('/v2/schedules/7', { name: '新名' });

    await deleteSchedule(7);
    expect(mocks.delete).toHaveBeenCalledWith('/v2/schedules/7');
  });

  it('gets one schedule', async () => {
    await fetchSchedule(9);
    expect(mocks.get).toHaveBeenCalledWith('/v2/schedules/9');
  });
});

describe('enable / disable / run-now', () => {
  it('hits the action endpoints', async () => {
    await enableSchedule(3);
    expect(mocks.post).toHaveBeenCalledWith('/v2/schedules/3/enable');

    await disableSchedule(3);
    expect(mocks.post).toHaveBeenCalledWith('/v2/schedules/3/disable');

    await runScheduleNow(3);
    expect(mocks.post).toHaveBeenCalledWith('/v2/schedules/3/run-now');
  });
});

describe('occurrences & preview', () => {
  it('lists occurrences with keyset params', async () => {
    await fetchScheduleOccurrences(5, 10, 30);
    expect(mocks.get).toHaveBeenCalledWith('/v2/schedules/5/occurrences', {
      limit: 30,
      before_id: 10,
    });
  });

  it('preview unwraps next_runs', async () => {
    mocks.post.mockResolvedValueOnce({ next_runs: ['2026-09-14T01:00:00Z'] });
    const runs = await previewScheduleRuns({ schedule_type: 'daily' });
    expect(mocks.post).toHaveBeenCalledWith('/v2/schedules/preview', { schedule_type: 'daily' });
    expect(runs).toEqual(['2026-09-14T01:00:00Z']);
  });

  it('preview tolerates a missing next_runs', async () => {
    mocks.post.mockResolvedValueOnce({});
    const runs = await previewScheduleRuns({ schedule_type: 'daily' });
    expect(runs).toEqual([]);
  });
});
