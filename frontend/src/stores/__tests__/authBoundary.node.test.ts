// @vitest-environment node

import { afterEach, expect, it, vi } from 'vitest';

class FakeBroadcastChannel {
  static constructed = 0;

  constructor() {
    FakeBroadcastChannel.constructed += 1;
  }
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
  FakeBroadcastChannel.constructed = 0;
});

it('does not open a native BroadcastChannel outside a browser window', async () => {
  vi.stubGlobal('BroadcastChannel', FakeBroadcastChannel);
  const boundary = await import('@/stores/authBoundary');
  const unsubscribe = boundary.subscribeExplicitLogout(() => {});

  expect(FakeBroadcastChannel.constructed).toBe(0);
  boundary.broadcastExplicitLogout();
  expect(FakeBroadcastChannel.constructed).toBe(0);
  unsubscribe();
});
