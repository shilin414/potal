// @vitest-environment jsdom

import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import NavigationSettingsModal from '@/components/Navigation/NavigationSettingsModal';
import { useNavigationPreferencesStore } from '@/stores/useNavigationPreferencesStore';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

const roots: Array<{ host: HTMLElement; root: Root }> = [];

beforeEach(() => {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false, media: query, onchange: null,
      addListener: vi.fn(), removeListener: vi.fn(),
      addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
    })),
  });
  useNavigationPreferencesStore.setState({ iconMode: 'outline', icons: {} });
});

afterEach(async () => {
  while (roots.length) {
    const { host, root } = roots.pop()!;
    await act(async () => root.unmount());
    host.remove();
  }
  document.body.innerHTML = '';
});

async function mountModal(isStaff = true) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push({ host, root });
  await act(async () => {
    root.render(
      <NavigationSettingsModal open onClose={() => undefined} isStaff={isStaff} />,
    );
  });
}

describe('NavigationSettingsModal', () => {
  it('shows each navigation item and the outline icon controls by default', async () => {
    await mountModal();
    expect(document.body.textContent).toContain('导航与外观');
    expect(document.body.textContent).toContain('对话');
    expect(document.body.textContent).toContain('企业控制台');
    expect(document.body.textContent).toContain('恢复默认图标');
  });

  it('hides staff-only navigation customization from non-staff users', async () => {
    await mountModal(false);
    expect(document.body.textContent).not.toContain('企业控制台');
  });

  it('explains emoji and text-only modes instead of showing irrelevant selectors', async () => {
    await mountModal();

    await act(async () => useNavigationPreferencesStore.getState().setIconMode('emoji'));
    expect(document.body.textContent).toContain('当前使用系统 Emoji 导航图标');
    expect(document.body.textContent).not.toContain('恢复默认图标');

    await act(async () => useNavigationPreferencesStore.getState().setIconMode('hidden'));
    expect(document.body.textContent).toContain('当前导航仅显示文字');
  });
});
