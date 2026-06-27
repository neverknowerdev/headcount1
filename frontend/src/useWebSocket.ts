import { useEffect, useRef } from 'react';

// useWebSocket connects to a WebSocket endpoint and reconnects automatically
// after any disconnect. Each call to onMessage receives exactly one parsed
// JSON object (matches the server guarantee of one JSON object per frame).
export function useWebSocket(
    url: string,
    onMessage: (msg: any) => void,
    enabled = true,
) {
    // Keep onMessage in a ref so we never need to restart the socket just
    // because the callback identity changed (common with inline arrow fns).
    const cbRef = useRef(onMessage);
    cbRef.current = onMessage;

    useEffect(() => {
        if (!enabled) return;

        let ws: WebSocket | null = null;
        let unmounted = false;
        let retryDelay = 1000;
        let retryTimer: ReturnType<typeof setTimeout> | null = null;

        function connect() {
            ws = new WebSocket(url);

            ws.onmessage = (event: MessageEvent) => {
                try {
                    cbRef.current(JSON.parse(event.data as string));
                } catch {
                    // ignore malformed frames
                }
            };

            ws.onopen = () => {
                retryDelay = 1000; // reset back-off on successful connect
            };

            ws.onclose = () => {
                if (unmounted) return;
                retryTimer = setTimeout(connect, retryDelay);
                retryDelay = Math.min(retryDelay * 2, 30_000); // cap at 30 s
            };

            ws.onerror = () => {
                // onclose fires after onerror, so reconnect is handled there
            };
        }

        connect();

        return () => {
            unmounted = true;
            if (retryTimer !== null) clearTimeout(retryTimer);
            ws?.close();
        };
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [url, enabled]);
}
