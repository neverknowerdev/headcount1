import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { renderHook, act, cleanup } from '@testing-library/react';
import { useWebSocket } from './useWebSocket';

// Scripted stand-in for the browser WebSocket. Tests drive the connection
// lifecycle explicitly: open(), receive(), serverClose().
class MockWebSocket {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSING = 2;
    static readonly CLOSED = 3;
    static instances: MockWebSocket[] = [];

    readonly CONNECTING = 0;
    readonly OPEN = 1;
    readonly CLOSING = 2;
    readonly CLOSED = 3;

    url: string;
    readyState = MockWebSocket.CONNECTING;
    onopen: (() => void) | null = null;
    onclose: (() => void) | null = null;
    onerror: (() => void) | null = null;
    onmessage: ((ev: { data: string }) => void) | null = null;
    closeCalls = 0;

    constructor(url: string) {
        this.url = url;
        MockWebSocket.instances.push(this);
    }

    open() {
        this.readyState = MockWebSocket.OPEN;
        this.onopen?.();
    }

    receive(obj: unknown) {
        this.onmessage?.({ data: typeof obj === 'string' ? obj : JSON.stringify(obj) });
    }

    serverClose() {
        this.readyState = MockWebSocket.CLOSED;
        this.onclose?.();
    }

    close() {
        this.closeCalls++;
        if (this.readyState !== MockWebSocket.CLOSED) {
            this.readyState = MockWebSocket.CLOSED;
            this.onclose?.();
        }
    }
}

const sockets = () => MockWebSocket.instances;
const lastSocket = () => MockWebSocket.instances[MockWebSocket.instances.length - 1];
const advance = (ms: number) => act(() => { vi.advanceTimersByTime(ms); });

describe('useWebSocket', () => {
    beforeEach(() => {
        vi.useFakeTimers();
        MockWebSocket.instances = [];
        vi.stubGlobal('WebSocket', MockWebSocket);
    });

    afterEach(() => {
        cleanup(); // unmount hooks so their window/document listeners don't leak across tests
        vi.unstubAllGlobals();
        vi.useRealTimers();
    });

    it('connects and calls onConnect on every successful open', () => {
        const onConnect = vi.fn();
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn(), { onConnect }));

        expect(sockets()).toHaveLength(1);
        act(() => lastSocket().open());
        expect(onConnect).toHaveBeenCalledTimes(1);

        act(() => lastSocket().serverClose());
        advance(1000); // first retry
        act(() => lastSocket().open());
        expect(onConnect).toHaveBeenCalledTimes(2);
    });

    it('does not connect when disabled', () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn(), { enabled: false }));
        expect(sockets()).toHaveLength(0);
    });

    it('delivers parsed messages, consumes heartbeats, ignores malformed frames', () => {
        const onMessage = vi.fn();
        renderHook(() => useWebSocket('ws://test/api/ws', onMessage));
        act(() => lastSocket().open());

        act(() => lastSocket().receive({ type: 'hub_heartbeat', payload: { ts: 1 } }));
        expect(onMessage).not.toHaveBeenCalled();

        act(() => lastSocket().receive('{not json'));
        expect(onMessage).not.toHaveBeenCalled();

        act(() => lastSocket().receive({ type: 'task_updated', payload: { id: 7 } }));
        expect(onMessage).toHaveBeenCalledWith({ type: 'task_updated', payload: { id: 7 } });
    });

    it('reconnects with exponential back-off while the server is down', () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        act(() => lastSocket().serverClose()); // connect refused

        advance(999);
        expect(sockets()).toHaveLength(1);
        advance(1); // 1s back-off
        expect(sockets()).toHaveLength(2);

        act(() => lastSocket().serverClose());
        advance(1999);
        expect(sockets()).toHaveLength(2);
        advance(1); // doubled to 2s
        expect(sockets()).toHaveLength(3);
    });

    it('resets the back-off after a successful connect', () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        act(() => lastSocket().serverClose());
        advance(1000);
        act(() => lastSocket().serverClose());
        advance(2000);
        act(() => lastSocket().open()); // success resets back-off
        act(() => lastSocket().serverClose());
        advance(1000); // back to 1s, not 4s
        expect(sockets()).toHaveLength(4);
    });

    it('recycles an OPEN socket that has gone silent (half-open connection)', () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        act(() => lastSocket().open());
        const first = lastSocket();

        advance(70_000); // > STALE_AFTER_MS, checked on the 10s watchdog tick
        expect(first.closeCalls).toBeGreaterThan(0);
        expect(sockets()).toHaveLength(2);
    });

    it('keeps a socket alive as long as heartbeats arrive', () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        act(() => lastSocket().open());

        for (let i = 0; i < 4; i++) {
            advance(25_000);
            act(() => lastSocket().receive({ type: 'hub_heartbeat', payload: {} }));
        }
        expect(sockets()).toHaveLength(1);
    });

    it('abandons a handshake stuck in CONNECTING (proxy that never upgrades)', () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        const stuck = lastSocket();
        expect(stuck.readyState).toBe(MockWebSocket.CONNECTING);

        advance(30_000); // CONNECT_TIMEOUT is 10s, watchdog ticks every 10s
        expect(sockets().length).toBeGreaterThan(1);
        expect(stuck.closeCalls).toBeGreaterThan(0);
    });

    it('reconnects immediately when the tab becomes visible with a dead socket', () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        act(() => lastSocket().open());
        act(() => lastSocket().serverClose()); // back-off timer now pending

        act(() => { document.dispatchEvent(new Event('visibilitychange')); });
        expect(sockets()).toHaveLength(2); // no waiting for the back-off

        act(() => lastSocket().open());
        advance(5_000); // the cancelled back-off timer (due at +1s) must not fire a third socket
        expect(sockets()).toHaveLength(2);
    });

    it('reconnects immediately when the browser comes back online', () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        act(() => lastSocket().open());
        const first = lastSocket();

        act(() => { window.dispatchEvent(new Event('online')); });
        expect(first.closeCalls).toBeGreaterThan(0);
        expect(sockets()).toHaveLength(2);
    });

    it('an abandoned socket that opens late does not double-fire onConnect', () => {
        const onConnect = vi.fn();
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn(), { onConnect }));
        const stuck = lastSocket();

        advance(30_000); // stuck-CONNECTING recycle kicks in
        const replacement = lastSocket();
        expect(replacement).not.toBe(stuck);

        act(() => stuck.onopen?.()); // late upgrade completion on the old socket
        act(() => replacement.open());
        expect(onConnect).toHaveBeenCalledTimes(1);
    });

    it('closes the socket and stops all reconnection on unmount', () => {
        const { unmount } = renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        act(() => lastSocket().open());
        const socket = lastSocket();

        unmount();
        expect(socket.closeCalls).toBe(1);

        advance(120_000);
        expect(sockets()).toHaveLength(1);
    });

    it('does not tear down the socket when the message handler identity changes', () => {
        const { rerender } = renderHook(
            ({ handler }) => useWebSocket('ws://test/api/ws', handler),
            { initialProps: { handler: vi.fn() } },
        );
        act(() => lastSocket().open());

        const nextHandler = vi.fn();
        rerender({ handler: nextHandler });
        expect(sockets()).toHaveLength(1);

        act(() => lastSocket().receive({ type: 'evt', payload: {} }));
        expect(nextHandler).toHaveBeenCalledTimes(1);
    });
});
