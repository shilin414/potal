// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest';

type MessageListener = (event: MessageEvent<unknown>) => void;

class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = [];

  readonly listeners = new Set<MessageListener>();
  readonly postMessage = vi.fn();
  readonly close = vi.fn();

  constructor(readonly name: string) {
    FakeBroadcastChannel.instances.push(this);
  }

  addEventListener(_type: 'message', listener: EventListenerOrEventListenerObject) {
    this.listeners.add(listener as MessageListener);
  }

  emit(data: unknown) {
    this.listeners.forEach((listener) => listener(new MessageEvent('message', { data })));
  }
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.resetModules();
  FakeBroadcastChannel.instances = [];
});

describe('auth boundary transport', () => {
  it('uses both transports, deduplicates delivery, and closes after unsubscribe', async () => {
    vi.stubGlobal('BroadcastChannel', FakeBroadcastChannel);
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    const removeItem = vi.spyOn(Storage.prototype, 'removeItem');
    const boundary = await import('@/stores/authBoundary');
    const listener = vi.fn();
    const unsubscribe = boundary.subscribeExplicitLogout(listener);

    expect(FakeBroadcastChannel.instances).toHaveLength(1);
    const channel = FakeBroadcastChannel.instances[0];
    expect(channel.name).toBe('studio-auth');

    boundary.broadcastExplicitLogout();
    const message = channel.postMessage.mock.calls[0][0];
    expect(message).toEqual(expect.objectContaining({
      type: 'explicit-logout',
      nonce: expect.any(String),
    }));
    expect(setItem).toHaveBeenCalledWith('studio-auth-event', JSON.stringify(message));
    expect(removeItem).toHaveBeenCalledWith('studio-auth-event');

    channel.emit(message);
    window.dispatchEvent(new StorageEvent('storage', {
      key: 'studio-auth-event',
      newValue: JSON.stringify(message),
    }));
    expect(listener).toHaveBeenCalledTimes(1);

    unsubscribe();
    expect(channel.close).toHaveBeenCalledTimes(1);
  });

  it('broadcasts identity changes to their own subscribers', async () => {
    vi.stubGlobal('BroadcastChannel', FakeBroadcastChannel);
    const boundary = await import('@/stores/authBoundary');
    const logoutListener = vi.fn();
    const identityListener = vi.fn();
    const unsubscribeLogout = boundary.subscribeExplicitLogout(logoutListener);
    const unsubscribeIdentity = boundary.subscribeIdentityChange(identityListener);
    const channel = FakeBroadcastChannel.instances[0];

    boundary.broadcastIdentityChange('7');
    const message = channel.postMessage.mock.calls[0][0];
    expect(message).toEqual(expect.objectContaining({
      type: 'identity-changed',
      nonce: expect.any(String),
      user_id: '7',
    }));

    channel.emit(message);
    window.dispatchEvent(new StorageEvent('storage', {
      key: 'studio-auth-event',
      newValue: JSON.stringify(message),
    }));
    expect(identityListener).toHaveBeenCalledOnce();
    expect(identityListener).toHaveBeenCalledWith('7');
    expect(logoutListener).not.toHaveBeenCalled();

    unsubscribeLogout();
    expect(channel.close).not.toHaveBeenCalled();
    unsubscribeIdentity();
    expect(channel.close).toHaveBeenCalledTimes(1);
  });

  it('uses storage when BroadcastChannel is unavailable', async () => {
    vi.stubGlobal('BroadcastChannel', undefined);
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    const removeItem = vi.spyOn(Storage.prototype, 'removeItem');
    const boundary = await import('@/stores/authBoundary');
    const listener = vi.fn();
    const unsubscribe = boundary.subscribeExplicitLogout(listener);

    boundary.broadcastExplicitLogout();

    expect(setItem).toHaveBeenCalledWith(
      'studio-auth-event',
      expect.stringContaining('explicit-logout'),
    );
    expect(removeItem).toHaveBeenCalledWith('studio-auth-event');

    window.dispatchEvent(new StorageEvent('storage', {
      key: 'studio-auth-event',
      newValue: JSON.stringify({ type: 'explicit-logout', nonce: 'remote' }),
    }));
    expect(listener).toHaveBeenCalledTimes(1);

    unsubscribe();
    window.dispatchEvent(new StorageEvent('storage', {
      key: 'studio-auth-event',
      newValue: JSON.stringify({ type: 'explicit-logout', nonce: 'late' }),
    }));
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it('continues with storage when BroadcastChannel postMessage throws', async () => {
    vi.stubGlobal('BroadcastChannel', FakeBroadcastChannel);
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    const boundary = await import('@/stores/authBoundary');
    const unsubscribe = boundary.subscribeExplicitLogout(() => {});
    FakeBroadcastChannel.instances[0].postMessage.mockImplementation(() => {
      throw new Error('channel closed');
    });

    expect(() => boundary.broadcastExplicitLogout()).not.toThrow();
    expect(setItem).toHaveBeenCalledWith(
      'studio-auth-event',
      expect.stringContaining('explicit-logout'),
    );
    unsubscribe();
  });
});
