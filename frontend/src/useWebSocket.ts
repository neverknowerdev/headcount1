/* eslint-disable @typescript-eslint/no-explicit-any */
import { useEffect, useRef } from 'react';
import ReactUseWebSocketModule from 'react-use-websocket';

// react-use-websocket ships CommonJS with only a default export; Vite's dep
// optimizer surfaces the module object as the default import while vitest's
// interop unwraps it to the hook. Handle both shapes.
const useReactWebSocket = (typeof ReactUseWebSocketModule === 'function'
    ? ReactUseWebSocketModule
    : (ReactUseWebSocketModule as { default: unknown }).default) as typeof ReactUseWebSocketModule;

// The server broadcasts an application-level hub_heartbeat every 25s (see
// eventhub). The library's heartbeat option treats ANY incoming message as
// liveness, so after STALE_AFTER_MS of silence it closes the socket — that is
// how a half-open (silently dead) connection gets recycled. The outgoing
// 'ping' text frame is read and discarded by the server.
const STALE_AFTER_MS = 60_000;
const HEARTBEAT_INTERVAL_MS = 25_000;
const MAX_RETRY_DELAY_MS = 15_000;
// A handshake that neither opens nor errors within this window (e.g. a proxy
// that accepts the TCP connection but never completes the upgrade) is closed
// and retried — the heartbeat can't cover this because it only starts on open.
const CONNECT_TIMEOUT_MS = 10_000;
const CONNECT_CHECK_INTERVAL_MS = 10_000;

// wsUrl builds the event-hub WebSocket URL for the current page, using wss://
// when the page itself is served over https.
export function wsUrl(): string {
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
    return `${proto}://${window.location.host}/api/ws`;
}

export interface UseWebSocketOptions {
    enabled?: boolean;
    // Called on every successful connect (including reconnects). Use this to
    // re-fetch state over HTTP so events missed while disconnected are never
    // lost — the database, not the socket, is the source of truth.
    onConnect?: () => void;
}

// useWebSocket maintains a durable connection to the event hub. Reconnection,
// exponential back-off, and half-open detection are delegated to
// react-use-websocket; this wrapper adds the event-hub protocol (one JSON
// object per frame, hub_heartbeat frames consumed internally) and a watchdog
// for handshakes stuck in CONNECTING, which the library does not time out.
export function useWebSocket(
    url: string,
    onMessage: (msg: any) => void,
    options: UseWebSocketOptions = {},
) {
    const { enabled = true, onConnect } = options;

    // Keep callbacks in refs so the socket never restarts just because a
    // callback identity changed (common with inline arrow fns).
    const onMessageRef = useRef(onMessage);
    const onConnectRef = useRef(onConnect);
    useEffect(() => {
        onMessageRef.current = onMessage;
        onConnectRef.current = onConnect;
    });

    const { getWebSocket } = useReactWebSocket(url, {
        shouldReconnect: () => true,
        reconnectAttempts: Infinity,
        reconnectInterval: (attempt) => Math.min(1000 * 2 ** attempt, MAX_RETRY_DELAY_MS),
        retryOnError: true,
        heartbeat: {
            message: 'ping',
            interval: HEARTBEAT_INTERVAL_MS,
            timeout: STALE_AFTER_MS,
        },
        onOpen: () => onConnectRef.current?.(),
        onMessage: (event) => {
            let msg: any;
            try {
                msg = JSON.parse(event.data as string);
            } catch {
                return; // ignore malformed frames
            }
            if (msg?.type === 'hub_heartbeat') return; // liveness only
            onMessageRef.current(msg);
        },
        // lastMessage state is unused (delivery goes through onMessage), so
        // skip the per-frame re-render it would otherwise cause.
        filter: () => false,
    }, enabled);

    // Watchdog for hung handshakes: closing a CONNECTING socket aborts it,
    // which fires the library's close handler and its normal reconnect path.
    useEffect(() => {
        if (!enabled) return;
        let connectingSince: number | null = null;
        const check = setInterval(() => {
            const ws = getWebSocket();
            if (ws && ws.readyState === WebSocket.CONNECTING) {
                connectingSince ??= Date.now();
                if (Date.now() - connectingSince > CONNECT_TIMEOUT_MS) {
                    connectingSince = null;
                    try { ws.close(); } catch { /* ignore */ }
                }
            } else {
                connectingSince = null;
            }
        }, CONNECT_CHECK_INTERVAL_MS);
        return () => clearInterval(check);
    }, [enabled, getWebSocket]);
}
