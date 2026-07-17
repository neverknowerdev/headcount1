import { useCallback, useEffect, useRef } from 'react';

// useCoalescedCallback returns a stable function that runs `fn` once,
// `delayMs` after the first call, no matter how many times it is invoked in
// that window — so a burst of WebSocket events becomes a single refetch.
// The latest `fn` is used when the timer fires (callers can pass an inline
// closure over fresh state), and a pending run is cancelled on unmount.
export function useCoalescedCallback(fn: () => void, delayMs = 250): () => void {
    const fnRef = useRef(fn);
    useEffect(() => { fnRef.current = fn; });

    const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    useEffect(() => () => {
        if (timerRef.current !== null) clearTimeout(timerRef.current);
    }, []);

    return useCallback(() => {
        if (timerRef.current !== null) return; // a run is already scheduled
        timerRef.current = setTimeout(() => {
            timerRef.current = null;
            fnRef.current();
        }, delayMs);
    }, [delayMs]);
}
