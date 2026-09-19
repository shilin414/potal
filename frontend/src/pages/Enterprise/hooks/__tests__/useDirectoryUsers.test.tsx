/**
 * useDirectoryUsers — disabled 废弃在途请求回归（三次复审 §43–§45）。
 *
 * 关闭 Picker / 切走 Users Tab 时，`enabled: false` 之前只挡「新」请求，
 * 在途响应返回后仍会 setItems/setError/setLoading —— 下次重开有机会短暂
 * 闪现上一次的搜索结果/错误。现在 disabled 时 bump request id，把在途
 * 响应整体作废。renderHook-style coverage via createRoot/act。
 * @vitest-environment jsdom
 */
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';
import { useDirectoryUsers, type UseDirectoryUsersOptions } from '../useDirectoryUsers';
import { enterpriseApi, type DirectoryUser } from '../../enterpriseApi';

vi.mock('../../enterpriseApi', () => ({
  enterpriseApi: {
    users: vi.fn(),
  },
}));

const mockUsers = vi.mocked(enterpriseApi.users);

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const user = (id: number, name: string): DirectoryUser => ({
  id, name, avatar_url: '', open_id: `o-${id}`, active_status: 1,
  is_resigned: false, local_user_id: null, is_active: true,
  departments: [{ id: 1, name: '财务部', is_primary: true }],
} as unknown as DirectoryUser);

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

interface Probe {
  current: ReturnType<typeof useDirectoryUsers> | null;
}

let host: HTMLElement;
let root: Root;
let latest: Probe;
let options: UseDirectoryUsersOptions;

function ProbeComponent() {
  latest.current = useDirectoryUsers(options);
  return null;
}

beforeEach(() => {
  mockUsers.mockReset();
  latest = { current: null };
  options = { query: '', enabled: true };
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => { root.unmount(); });
  host.remove();
});

describe('useDirectoryUsers — disabled invalidation (§43–§45)', () => {
  it('a response arriving AFTER the surface went inactive is ignored', async () => {
    let resolveUsers!: (page: { results: DirectoryUser[]; next_cursor: string | null }) => void;
    mockUsers.mockReturnValueOnce(new Promise((resolve) => {
      resolveUsers = resolve;
    }));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.loading).toBe(true);

    // Close the surface while the request is still travelling.
    options = { ...options, enabled: false };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.loading).toBe(false); // spinner stopped

    // The stale response lands now — it must be ignored entirely.
    await act(async () => {
      resolveUsers({ results: [user(1, '张三')], next_cursor: null });
    });
    await flush(10);
    expect(latest.current!.items).toEqual([]); // nothing landed
    expect(latest.current!.loading).toBe(false);
  });

  it('a failure arriving after the surface went inactive is ignored too', async () => {
    let rejectUsers!: (reason?: unknown) => void;
    mockUsers.mockReturnValueOnce(new Promise((_resolve, reject) => {
      rejectUsers = reject;
    }));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    options = { ...options, enabled: false };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    await act(async () => { rejectUsers(new Error('closed')); });
    await flush(10);
    expect(latest.current!.error).toBeNull(); // no stale error to flash on reopen
  });
});
