import { describe, it, expect } from 'vitest';
import { reduceJobEvent, type JobState } from '../useJob';

const initial: JobState = {
  status: 'pending', progress: { current: 0, total: 0, label: '' },
  items: [], logs: [],
};

describe('reduceJobEvent', () => {
  it('applies job.state', () => {
    const s = reduceJobEvent(initial, { type: 'job.state', status: 'running' });
    expect(s.status).toBe('running');
  });

  it('applies job.progress', () => {
    const s = reduceJobEvent(initial, { type: 'job.progress', current: 2, total: 5, label: 'a' });
    expect(s.progress).toEqual({ current: 2, total: 5, label: 'a' });
  });

  it('appends item.state (upserts by id)', () => {
    let s = reduceJobEvent(initial, { type: 'item.state', id: 'v1', name: 'a', status: 'done', result: '/o', error: '' });
    s = reduceJobEvent(s, { type: 'item.state', id: 'v1', name: 'a', status: 'error', result: '', error: 'boom' });
    expect(s.items).toHaveLength(1);
    expect(s.items[0].status).toBe('error');
  });

  it('appends log capped at 500', () => {
    let s = initial;
    for (let i = 0; i < 600; i++) s = reduceJobEvent(s, { type: 'log', level: 'info', msg: String(i) });
    expect(s.logs).toHaveLength(500);
    expect(s.logs[499].msg).toBe('599');
  });

  it('state.snapshot bulk-sets all fields', () => {
    const s = reduceJobEvent(initial, {
      type: 'state.snapshot', status: 'running',
      progress: { current: 1, total: 3, label: 'x' },
      items: [{ id: 'v1', name: 'a', status: 'done', result: '', error: '' }],
      logs: [{ level: 'info', msg: 'hi' }],
    });
    expect(s.status).toBe('running');
    expect(s.items).toHaveLength(1);
    expect(s.logs[0].msg).toBe('hi');
  });
});
