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

vi.mock('@/services/runApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/services/runApi')>()),
  resolveApplication: mocks.resolve,
}));
vi.mock('@/pages/Apps/ApplicationRuntimePage', () => ({
  default: () => <div>legacy runtime</div>,
}));

import LegacyAppRunRedirect from '../LegacyAppRunRedirect';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { resetSessionScopedState } from '@/stores/resetSessionState';
import type { V2Application } from '@/services/runApi';

const roots: Root[] = [];
const app = (id: number, slug: string): V2Application => ({
  id, slug, name: slug, description: '', icon: '', kind: 'chat',
  runtime_type: 'agent', provider_key: 'feishu_aily', capabilities: {},
});

function LocationProbe() {
  const location = useLocation();
  return <div data-location={`${location.pathname}${location.search}`} />;
}

function renderLegacy(path: string) {
  const host = document.createElement('div');
  const root = createRoot(host);
  roots.push(root);
  act(() => {
    root.render(
      <MemoryRouter initialEntries={[path]}>
        <LocationProbe />
        <Routes>
          <Route path="/apps/:id/run" element={<LegacyAppRunRedirect />} />
          <Route path="/chat/:applicationSlug" element={<div>chat target</div>} />
          <Route path="/" element={<div>home target</div>} />
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

describe('LegacyAppRunRedirect consume admission', () => {
  it('does not redirect from a cached entity until this lookup succeeds', async () => {
    let resolve: ((application: V2Application) => void) | undefined;
    mocks.resolve.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
    useApplicationEntityStore.getState().upsertManage(app(7, 'cached'));

    const host = renderLegacy('/apps/7/run?conversation=12');

    expect(host.textContent).not.toContain('chat target');
    expect(host.querySelector('[data-location]')?.getAttribute('data-location'))
      .toBe('/apps/7/run?conversation=12');

    await act(async () => { resolve?.(app(7, 'admitted')); });
    await settle();
    expect(host.textContent).toContain('chat target');
    expect(host.querySelector('[data-location]')?.getAttribute('data-location'))
      .toBe('/chat/admitted?conversation=12');
  });

  it('keeps the legacy URL and shows retry UI after a transport failure', async () => {
    const error: any = new Error('server down');
    error.response = { status: 500 };
    mocks.resolve.mockRejectedValueOnce(error);
    useApplicationEntityStore.getState().upsertManage(app(8, 'unstable'));

    const host = renderLegacy('/apps/8/run');
    await settle();

    expect(host.textContent).toContain('应用加载失败');
    expect(host.querySelector('[data-location]')?.getAttribute('data-location'))
      .toBe('/apps/8/run');
  });
});
