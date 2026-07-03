/* eslint-disable @typescript-eslint/no-explicit-any */
import { useEffect, useRef } from 'react';

// Server emits an application-level heartbeat every 25s (see eventhub).
// If we see no message at all for STALE_AFTER_MS, the connection is assumed
// half-open (dead TCP that never fires onclose) and is forcibly recycled.
const STALE_AFTER_MS = 60_000;
const STALE_CHECK_INTERVAL_MS = 10_000;
const MAX_RETRY_DELAY_MS = 15_000;

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

// useWebSocket maintains a durable connection to the event hub:
//  - reconnects automatically with capped exponential back-off;
//  - detects half-open connections via the server heartbeat and recycles them;
//  - reconnects immediately when the tab becomes visible or the browser
//    comes back online;
//  - invokes onConnect after every (re)connect so callers can re-sync state.
// Each call to onMessage receives exactly one parsed JSON object (matches the
// server guarantee of one JSON object per frame). Heartbeat frames are
// consumed internally and not passed to onMessage.
export function useWebSocket(
    url: string,
    onMessage: (msg: any) => void,
    options: UseWebSocketOptions = {},
) {
    const { enabled = true, onConnect } = options;

    // Keep callbacks in refs so we never need to restart the socket just
    // because a callback identity changed (common with inline arrow fns).
    const onMessageRef = useRef(onMessage);
    const onConnectRef = useRef(onConnect);
    useEffect(() => {
        onMessageRef.current = onMessage;
        onConnectRef.current = onConnect;
    });

    useEffect(() => {
        if (!enabled) return;

        let ws: WebSocket | null = null;
        let unmounted = false;
        let retryDelay = 1000;
        let retryTimer: ReturnType<typeof setTimeout> | null = null;
        let lastMessageAt = Date.now();

        function connect() {
            retryTimer = null;
            ws = new WebSocket(url);
            lastMessageAt = Date.now();

            ws.onmessage = (event: MessageEvent) => {
                lastMessageAt = Date.now();
                let msg: any;
                try {
                    msg = JSON.parse(event.data as string);
                } catch {
                    return; // ignore malformed frames
                }
                if (msg?.type === 'hub_heartbeat') return; // liveness only
                onMessageRef.current(msg);
            };

            ws.onopen = () => {
                retryDelay = 1000; // reset back-off on successful connect
                lastMessageAt = Date.now();
                onConnectRef.current?.();
            };

            ws.onclose = () => {
                ws = null;
                if (unmounted) return;
                retryTimer = setTimeout(connect, retryDelay);
                retryDelay = Math.min(retryDelay * 2, MAX_RETRY_DELAY_MS);
            };

            ws.onerror = () => {
                // onclose fires after onerror, so reconnect is handled there
            };
        }

        function reconnectNow() {
            if (unmounted) return;
            if (retryTimer !== null) {
                clearTimeout(retryTimer);
                retryTimer = null;
            }
            if (ws) {
                // Closing triggers onclose; connect immediately instead of
                // waiting out the back-off.
                const old = ws;
                ws = null;
                old.onclose = null;
                old.onmessage = null;
                try { old.close(); } catch { /* ignore */ }
            }
            connect();
        }

        // Watchdog: a half-open connection never fires onclose, so if the
        // server heartbeat goes quiet the socket is dead — recycle it.
        const staleCheck = setInterval(() => {
            if (ws && ws.readyState === WebSocket.OPEN
                && Date.now() - lastMessageAt > STALE_AFTER_MS) {
                reconnectNow();
            }
        }, STALE_CHECK_INTERVAL_MS);

        // When the tab wakes up or the network returns, don't wait for the
        // back-off timer or the watchdog — resync immediately.
        const onVisible = () => {
            if (document.visibilityState !== 'visible') return;
            const dead = !ws || ws.readyState === WebSocket.CLOSED || ws.readyState === WebSocket.CLOSING;
            const stale = Date.now() - lastMessageAt > STALE_AFTER_MS;
            if (dead || stale) reconnectNow();
        };
        const onOnline = () => reconnectNow();
        document.addEventListener('visibilitychange', onVisible);
        window.addEventListener('online', onOnline);

        connect();

        return () => {
            unmounted = true;
            clearInterval(staleCheck);
            document.removeEventListener('visibilitychange', onVisible);
            window.removeEventListener('online', onOnline);
            if (retryTimer !== null) clearTimeout(retryTimer);
            ws?.close();
        };
    }, [url, enabled]);
}
