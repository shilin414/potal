/**
 * MobilePermissionEditor — 请求契约与稳定性回归（二次复审 P2-4/P2-5）。
 *
 *   · opening the editor requests access + departments ONLY — the old
 *     enterpriseApi.users({ limit: 100 }) prefetch was dead weight (the user
 *     picker queries users itself);
 *   · the load effect depends on application.id, not the application object —
 *     a parent re-render handing a NEW { id, name } literal must not reset the
 *     policy mid-edit;
 *   · the save payload is exactly { access_mode, department_grants, user_grants }.
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  access: vi.fn(),
  departments: vi.fn(),
  users: vi.fn(),
  updateAccess: vi.fn(),
  messageError: vi.fn(),
  messageSuccess: vi.fn(),
}));

vi.mock('../../enterpriseApi', () => ({
  enterpriseApi: {
    access: mocks.access,
    departments: mocks.departments,
    users: mocks.users,
    updateAccess: mocks.updateAccess,
  },
}));

vi.mock('antd', () => {
  const RadioMock = ({ children }: { children?: React.ReactNode }) => (
    <label>{children}</label>
  );
  const RadioGroupMock = ({ children, value }: {
    children?: React.ReactNode;
    value?: string;
  }) => (
    <div data-testid="radio-group" data-value={value}>{children}</div>
  );
  (RadioMock as unknown as { Group: unknown }).Group = RadioGroupMock;
  return {
    Empty: ({ description }: { description?: React.ReactNode }) => (
      <div data-testid="empty">{description}</div>
    ),
    Radio: RadioMock,
    Skeleton: () => <div data-testid="skeleton" />,
    Switch: ({ checked, onChange }: { checked?: boolean; onChange?: (v: boolean) => void }) => (
      <button type="button" data-testid="switch" onClick={() => onChange?.(!checked)}>
        {checked ? 'on' : 'off'}
      </button>
    ),
    message: { success: mocks.messageSuccess, error: mocks.messageError },
  };
});

vi.mock('@/components/MobileConsole', () => ({
  MobileFullScreenDrawer: ({ open, title, actionText, onAction, children }: {
    open: boolean;
    title?: string;
    actionText?: string;
    onAction?: () => void;
    children?: React.ReactNode;
  }) => (
    open
      ? (
        <div data-testid="fs-drawer">
          <span data-testid="fs-title">{title}</span>
          <button type="button" data-testid="fs-action" onClick={onAction}>
            {actionText}
          </button>
          {children}
        </div>
      )
      : null
  ),
  MobileSection: ({ title, children }: { title?: string; children?: React.ReactNode }) => (
    <section data-testid="section"><h3>{title}</h3>{children}</section>
  ),
}));

vi.mock('../MobileDepartmentPicker', () => ({
  default: () => <div data-testid="dep-picker" />,
}));
vi.mock('../MobileUserPicker', () => ({
  default: () => <div data-testid="user-picker" />,
}));

import MobilePermissionEditor from '../MobilePermissionEditor';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};
(globalThis as unknown as { matchMedia: unknown }).matchMedia = (query: string) => ({
  matches: false, media: query, onchange: null,
  addListener() {}, removeListener() {},
  addEventListener() {}, removeEventListener() {},
  dispatchEvent: () => false,
});

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

const mounted: Array<{ host: HTMLElement; root: Root }> = [];
async function mountEditor(application: { id: number; name: string } | null) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  mounted.push({ host, root });
  await act(async () => {
    root.render(
      <MobilePermissionEditor open application={application} onClose={() => {}} />,
    );
  });
  await flush(20);
  return { host, root };
}

afterEach(async () => {
  while (mounted.length) {
    const { host, root } = mounted.pop()!;
    await act(async () => { root.unmount(); });
    host.remove();
  }
  document.body.innerHTML = '';
});

const POLICY = {
  application_id: 7,
  access_mode: 'assigned',
  departments: [{ department_id: 3, name: '财务部', include_children: true, covered_users: 12 }],
  users: [{ directory_user_id: 9, name: '张三', avatar_url: '', departments: ['财务部'] }],
};

beforeEach(() => {
  mocks.access.mockReset().mockResolvedValue(POLICY);
  mocks.departments.mockReset().mockResolvedValue([]);
  mocks.users.mockReset();
  mocks.updateAccess.mockReset();
  mocks.messageError.mockReset();
  mocks.messageSuccess.mockReset();
});

describe('MobilePermissionEditor — request contract (P2-4)', () => {
  it('opening loads access + departments and never prefetches users', async () => {
    await mountEditor({ id: 7, name: '财务助手' });

    expect(mocks.access).toHaveBeenCalledWith(7);
    expect(mocks.departments).toHaveBeenCalledTimes(1);
    expect(mocks.users).not.toHaveBeenCalled();
  });
});

describe('MobilePermissionEditor — re-render stability (P2-5)', () => {
  it('a parent re-render with a NEW application literal does not reload or reset the policy', async () => {
    const { root } = await mountEditor({ id: 7, name: '财务助手' });
    expect(mocks.access).toHaveBeenCalledTimes(1);

    // Same id, fresh object identity — exactly what MobileAccessPage produces
    // on every parent render (`selected ? { id, name } : null`).
    await act(async () => {
      root.render(
        <MobilePermissionEditor open application={{ id: 7, name: '财务助手' }} onClose={() => {}} />,
      );
    });
    await flush(20);

    expect(mocks.access).toHaveBeenCalledTimes(1); // NOT reloaded
    // The loaded policy is still on screen.
    expect(document.body.textContent).toContain('财务部');
    expect(document.body.textContent).toContain('张三');
  });
});

describe('MobilePermissionEditor — save payload', () => {
  it('saves exactly { access_mode, department_grants, user_grants }', async () => {
    const onClose = vi.fn();
    const { root } = await mountEditor({ id: 7, name: '财务助手' });
    await act(async () => {
      root.render(
        <MobilePermissionEditor open application={{ id: 7, name: '财务助手' }} onClose={onClose} />,
      );
    });
    mocks.updateAccess.mockResolvedValue(POLICY);

    await act(async () => {
      document.querySelector<HTMLButtonElement>('[data-testid="fs-action"]')!.click();
    });
    await flush(20);

    expect(mocks.updateAccess).toHaveBeenCalledWith(7, {
      access_mode: 'assigned',
      department_grants: [{ department_id: 3, include_children: true }],
      user_grants: [9],
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
