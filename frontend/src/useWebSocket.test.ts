import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { renderHook, act, cleanup } from '@testing-library/react';
import { useWebSocket } from './useWebSocket';

// react-use-websocket opens its socket in an effect; renderHook only flushes
// effects synchronously when the act environment is enabled.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Scripted stand-in for the browser WebSocket. Tests drive the connection
// lifecycle explicitly: open(), receive(), serverClose(). Must look enough
// like a real WebSocket for react-use-websocket: instanceof global WebSocket,
// send() (heartbeat pings), addEventListener (heartbeat cleanup).
class MockWebSocket extends EventTarget {
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
    onopen: ((ev: Event) => void) | null = null;
    onclose: ((ev: Event) => void) | null = null;
    onerror: ((ev: Event) => void) | null = null;
    onmessage: ((ev: { data: string }) => void) | null = null;
    closeCalls = 0;
    sent: string[] = [];

    constructor(url: string) {
        super();
        this.url = url;
        MockWebSocket.instances.push(this);
    }

    send(data: string) {
        this.sent.push(data);
    }

    open() {
        this.readyState = MockWebSocket.OPEN;
        this.onopen?.(new Event('open'));
    }

    receive(obj: unknown) {
        this.onmessage?.({ data: typeof obj === 'string' ? obj : JSON.stringify(obj) });
    }

    serverClose() {
        this.readyState = MockWebSocket.CLOSED;
        this.onclose?.(new Event('close'));
        this.dispatchEvent(new Event('close'));
    }

    close() {
        this.closeCalls++;
        if (this.readyState !== MockWebSocket.CLOSED) {
            this.readyState = MockWebSocket.CLOSED;
            this.onclose?.(new Event('close'));
            this.dispatchEvent(new Event('close'));
        }
    }
}

const sockets = () => MockWebSocket.instances;
const lastSocket = () => MockWebSocket.instances[MockWebSocket.instances.length - 1];
// The library resolves its URL asynchronously before opening a socket, so
// renders and timer advances must flush microtasks too.
const flush = () => act(async () => {});
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });

describe('useWebSocket', () => {
    beforeEach(() => {
        vi.useFakeTimers();
        MockWebSocket.instances = [];
        vi.stubGlobal('WebSocket', MockWebSocket);
        vi.spyOn(console, 'warn').mockImplementation(() => {}); // heartbeat-timeout noise
    });

    afterEach(() => {
        cleanup(); // unmount hooks so their timers don't leak across tests
        vi.unstubAllGlobals();
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    it('connects and calls onConnect on every successful open', async () => {
        const onConnect = vi.fn();
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn(), { onConnect }));
        await flush();

        expect(sockets()).toHaveLength(1);
        act(() => lastSocket().open());
        expect(onConnect).toHaveBeenCalledTimes(1);

        act(() => lastSocket().serverClose());
        await advance(1000); // first retry
        act(() => lastSocket().open());
        expect(onConnect).toHaveBeenCalledTimes(2);
    });

    it('does not connect when disabled', async () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn(), { enabled: false }));
        await flush();
        expect(sockets()).toHaveLength(0);
    });

    it('delivers parsed messages, consumes heartbeats, ignores malformed frames', async () => {
        const onMessage = vi.fn();
        renderHook(() => useWebSocket('ws://test/api/ws', onMessage));
        await flush();
        act(() => lastSocket().open());

        act(() => lastSocket().receive({ type: 'hub_heartbeat', payload: { ts: 1 } }));
        expect(onMessage).not.toHaveBeenCalled();

        act(() => lastSocket().receive('{not json'));
        expect(onMessage).not.toHaveBeenCalled();

        act(() => lastSocket().receive({ type: 'task_updated', payload: { id: 7 } }));
        expect(onMessage).toHaveBeenCalledWith({ type: 'task_updated', payload: { id: 7 } });
    });

    it('reconnects with exponential back-off while the server is down', async () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        await flush();
        act(() => lastSocket().serverClose()); // connect refused

        await advance(999);
        expect(sockets()).toHaveLength(1);
        await advance(1); // 1s back-off
        expect(sockets()).toHaveLength(2);

        act(() => lastSocket().serverClose());
        await advance(1999);
        expect(sockets()).toHaveLength(2);
        await advance(1); // doubled to 2s
        expect(sockets()).toHaveLength(3);
    });

    it('resets the back-off after a successful connect', async () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        await flush();
        act(() => lastSocket().serverClose());
        await advance(1000);
        act(() => lastSocket().serverClose());
        await advance(2000);
        act(() => lastSocket().open()); // success resets back-off
        act(() => lastSocket().serverClose());
        await advance(1000); // back to 1s, not 4s
        expect(sockets()).toHaveLength(4);
    });

    it('recycles an OPEN socket that has gone silent (half-open connection)', async () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        await flush();
        act(() => lastSocket().open());
        const first = lastSocket();

        // Heartbeat timeout is 60s; the check runs every interval/10, and the
        // reconnect follows within the 1s back-off.
        await advance(70_000);
        expect(first.closeCalls).toBeGreaterThan(0);
        expect(sockets()).toHaveLength(2);
    });

    it('keeps a socket alive as long as server heartbeats arrive', async () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        await flush();
        act(() => lastSocket().open());

        for (let i = 0; i < 4; i++) {
            await advance(25_000);
            act(() => lastSocket().receive({ type: 'hub_heartbeat', payload: {} }));
        }
        expect(sockets()).toHaveLength(1);
    });

    it('abandons a handshake stuck in CONNECTING (proxy that never upgrades)', async () => {
        renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        await flush();
        const stuck = lastSocket();
        expect(stuck.readyState).toBe(MockWebSocket.CONNECTING);

        // The watchdog ticks every 10s and closes a socket that has been
        // CONNECTING for over 10s; the reconnect follows on the back-off.
        await advance(40_000);
        expect(stuck.closeCalls).toBeGreaterThan(0);
        expect(sockets().length).toBeGreaterThan(1);
    });

    it('closes the socket and stops all reconnection on unmount', async () => {
        const { unmount } = renderHook(() => useWebSocket('ws://test/api/ws', vi.fn()));
        await flush();
        act(() => lastSocket().open());
        const socket = lastSocket();

        unmount();
        expect(socket.closeCalls).toBeGreaterThan(0);

        await advance(120_000);
        expect(sockets()).toHaveLength(1);
    });

    it('does not tear down the socket when the message handler identity changes', async () => {
        const { rerender } = renderHook(
            ({ handler }) => useWebSocket('ws://test/api/ws', handler),
            { initialProps: { handler: vi.fn() } },
        );
        await flush();
        act(() => lastSocket().open());

        const nextHandler = vi.fn();
        rerender({ handler: nextHandler });
        expect(sockets()).toHaveLength(1);

        act(() => lastSocket().receive({ type: 'evt', payload: {} }));
        expect(nextHandler).toHaveBeenCalledTimes(1);
    });
});
