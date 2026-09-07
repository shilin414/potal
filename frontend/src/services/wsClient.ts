/**
 * Minimal reconnecting WebSocket client.
 * Browser WS can't set headers, so auth is passed via ?token= by the caller.
 */
export interface WsClientOptions {
  onMessage: (event: any) => void;
  onOpen?: () => void;
  onClose?: () => void;
  /** Base reconnect backoff (ms). Default 1000, capped at 15000. */
  reconnectDelay?: number;
}

export interface WsClient {
  send(obj: unknown): void;
  close(): void;
}

export function createWsClient(url: string, opts: WsClientOptions): WsClient {
  const { onMessage, onOpen, onClose, reconnectDelay = 1000 } = opts;
  let ws: WebSocket | null = null;
  let manuallyClosed = false;
  let delay = reconnectDelay;

  const connect = () => {
    ws = new WebSocket(url);

    ws.addEventListener('open', () => {
      delay = reconnectDelay;
      onOpen?.();
    });

    ws.addEventListener('message', (e: MessageEvent) => {
      try {
        onMessage(JSON.parse(e.data));
      } catch {
        // ignore malformed frames
      }
    });

    ws.addEventListener('close', () => {
      onClose?.();
      if (!manuallyClosed) {
        const wait = delay;
        delay = Math.min(delay * 2, 15000);
        setTimeout(connect, wait);
      }
    });

    ws.addEventListener('error', () => {
      // let onclose handle reconnect
    });
  };

  connect();

  return {
    send(obj) {
      if (ws && ws.readyState === 1 /* WebSocket.OPEN */) {
        ws.send(JSON.stringify(obj));
      }
    },
    close() {
      manuallyClosed = true;
      ws?.close();
    },
  };
}
