import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { renderHook, act, cleanup } from '@testing-library/react';
import { useCoalescedCallback } from './useCoalescedCallback';

describe('useCoalescedCallback', () => {
    beforeEach(() => { vi.useFakeTimers(); });
    afterEach(() => { cleanup(); vi.useRealTimers(); });

    it('coalesces a burst of calls into one deferred run', () => {
        const fn = vi.fn();
        const { result } = renderHook(() => useCoalescedCallback(fn, 250));

        act(() => { result.current(); result.current(); result.current(); });
        expect(fn).not.toHaveBeenCalled(); // deferred, not immediate

        act(() => { vi.advanceTimersByTime(250); });
        expect(fn).toHaveBeenCalledTimes(1);
    });

    it('schedules again after the previous run fired', () => {
        const fn = vi.fn();
        const { result } = renderHook(() => useCoalescedCallback(fn, 250));

        act(() => { result.current(); });
        act(() => { vi.advanceTimersByTime(250); });
        act(() => { result.current(); });
        act(() => { vi.advanceTimersByTime(250); });
        expect(fn).toHaveBeenCalledTimes(2);
    });

    it('invokes the latest callback, not the one captured at schedule time', () => {
        const first = vi.fn();
        const second = vi.fn();
        const { result, rerender } = renderHook(
            ({ fn }) => useCoalescedCallback(fn, 250),
            { initialProps: { fn: first } },
        );

        act(() => { result.current(); });
        rerender({ fn: second }); // e.g. fetchTasks identity changed with new filters
        act(() => { vi.advanceTimersByTime(250); });

        expect(first).not.toHaveBeenCalled();
        expect(second).toHaveBeenCalledTimes(1);
    });

    it('cancels a pending run on unmount', () => {
        const fn = vi.fn();
        const { result, unmount } = renderHook(() => useCoalescedCallback(fn, 250));

        act(() => { result.current(); });
        unmount();
        act(() => { vi.advanceTimersByTime(1000); });
        expect(fn).not.toHaveBeenCalled();
    });
});
