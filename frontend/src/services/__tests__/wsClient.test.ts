import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

describe('wsClient', () => {
  let makeWs: () => any;
  let instances: any[];

  beforeEach(() => {
    instances = [];
    makeWs = () => {
      const inst = {
        url: '',
        readyState: 0,
        OPEN: 1,
        send: vi.fn(),
        close: vi.fn(),
        addEventListener: vi.fn((evt: string, cb: any) => { inst._cbs[evt] = cb; }),
        _cbs: {} as Record<string, any>,
        _open() { this.readyState = 1; this._cbs['open']?.(); },
        _message(data: any) { this._cbs['message']?.({ data: JSON.stringify(data) }); },
        _close() { this.readyState = 3; this._cbs['close']?.({ wasClean: true }); },
      };
      instances.push(inst);
      return inst;
    };
    (globalThis as any).WebSocket = vi.fn(function MockWebSocket(url: string) {
      const ws = makeWs();
      ws.url = url;
      return ws;
    });
  });

  afterEach(() => { vi.useRealTimers(); });

  it('dispatches parsed messages to onMessage', async () => {
    vi.useFakeTimers();
    const { createWsClient } = await import('../wsClient');
    const onMessage = vi.fn();
    const c = createWsClient('ws://x/ws/runner/j/?token=t', { onMessage });
    instances[0]._open();
    instances[0]._message({ type: 'log', msg: 'hi' });
    expect(onMessage).toHaveBeenCalledWith({ type: 'log', msg: 'hi' });
    c.close();
  });

  it('sends JSON via send()', async () => {
    const { createWsClient } = await import('../wsClient');
    const c = createWsClient('ws://x/ws/runner/j/?token=t', { onMessage: () => {} });
    instances[0]._open();
    c.send({ action: 'stop' });
    expect(instances[0].send).toHaveBeenCalledWith(JSON.stringify({ action: 'stop' }));
    c.close();
  });
});
