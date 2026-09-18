// @vitest-environment jsdom

import React from 'react';
import { act } from 'react-dom/test-utils';
import { createRoot, type Root } from 'react-dom/client';
import {
  MemoryRouter,
  Route,
  Routes,
  useLocation,
} from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ resolve: vi.fn() }));

vi.mock('@/services/runApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/runApi')>();
  return { ...actual, resolveApplication: mocks.resolve };
});
vi.mock('../ChatRenderer', () => ({
  default: ({ application }: any) => <div data-renderer="chat">{application.name}</div>,
}));
vi.mock('../PageRenderer', () => ({
  default: ({ application }: any) => <div data-renderer="page">{application.name}</div>,
}));
vi.mock('../WorkflowRenderer', () => ({
  default: ({ application }: any) => <div data-renderer="workflow">{application.name}</div>,
}));
vi.mock('../HomeWorkspace', () => ({ default: () => <div>home</div> }));

import WorkspaceHost from '../WorkspaceHost';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { resetSessionScopedState } from '@/stores/resetSessionState';
import type { V2Application } from '@/services/runApi';

const roots: Root[] = [];
const app = (id: number, slug: string, kind: 'chat' | 'fixed'): V2Application => ({
  id, slug, name: `app-${id}`, description: '', icon: '', kind,
  renderer_key: kind === 'chat' ? 'chat' : 'page',
  runtime_type: kind === 'chat' ? 'agent' : 'fixed',
  provider_key: kind === 'chat' ? 'feishu_aily' : '',
  capabilities: {},
});
const httpError = (status: number): unknown => {
  const error: any = new Error(`request failed with ${status}`);
  error.response = { status };
  return error;
};

function LocationProbe() {
  const location = useLocation();
  return <div data-location={`${location.pathname}${location.search}`} />;
}

function renderRoute(path: string) {
  const host = document.createElement('div');
  const root = createRoot(host);
  roots.push(root);
  act(() => {
    root.render(
      <MemoryRouter initialEntries={[path]}>
        <LocationProbe />
        <Routes>
          <Route path="/chat/:applicationSlug" element={<WorkspaceHost kind="chat" />} />
          <Route path="/app/:applicationSlug" element={<WorkspaceHost kind="page" />} />
        </Routes>
      </MemoryRouter>,
    );
  });
  return host;
}

async function settle() {
  await act(async () => { await Promise.resolve(); });
}

beforeEach(() => {
  mocks.resolve.mockReset();
  resetSessionScopedState();
});

afterEach(() => {
  while (roots.length) roots.pop()?.unmount();
});

describe('WorkspaceHost consume admission', () => {
  it('does not mount a cached chat renderer while forced resolve is pending', async () => {
    let reject: ((error: unknown) => void) | undefined;
    mocks.resolve.mockReturnValueOnce(new Promise((_resolve, rejectPromise) => {
      reject = rejectPromise;
    }));
    useApplicationEntityStore.getState().upsertManage(app(1, 'sales', 'chat'));

    const host = renderRoute('/chat/sales');

    expect(host.querySelector('[data-renderer="chat"]')).toBeNull();
    await act(async () => { reject?.(httpError(404)); });
    await settle();
    expect(host.querySelector('[data-renderer="chat"]')).toBeNull();
    expect(host.textContent).toContain('找不到应用：sales');
    expect(useApplicationEntityStore.getState().bySlug.sales).toBeUndefined();
  });

  it('does not mount a cached fixed renderer until this entry is admitted', async () => {
    let resolve: ((application: V2Application) => void) | undefined;
    mocks.resolve.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
    useApplicationEntityStore.getState().upsertManage(app(2, 'dashboard', 'fixed'));

    const host = renderRoute('/app/dashboard');

    expect(host.querySelector('[data-renderer="page"]')).toBeNull();
    await act(async () => { resolve?.(app(2, 'dashboard', 'fixed')); });
    await settle();
    expect(host.querySelector('[data-renderer="page"]')?.textContent).toBe('app-2');
  });

  it('keeps the URL and renderer unmounted on a transport failure', async () => {
    mocks.resolve.mockRejectedValueOnce(httpError(500));
    useApplicationEntityStore.getState().upsertManage(app(3, 'unstable', 'chat'));

    const host = renderRoute('/chat/unstable?conversation=12');
    await settle();

    expect(host.querySelector('[data-renderer="chat"]')).toBeNull();
    expect(host.textContent).toContain('应用加载失败');
    expect(host.querySelector('[data-location]')?.getAttribute('data-location'))
      .toBe('/chat/unstable?conversation=12');
  });
});
