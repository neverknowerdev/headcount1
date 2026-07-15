import { describe, it, expect } from 'vitest';
import { mergeSnapshotWithLiveTail, type LogMessage } from './logMerge';

const msg = (ts: string | undefined, content: string, id = 0): LogMessage => ({
    id,
    entry: ts === undefined ? { content } : { ts, content },
});

const contents = (msgs: LogMessage[]) => msgs.map((m) => m.entry.content);

describe('mergeSnapshotWithLiveTail', () => {
    it('returns the snapshot when nothing is on screen yet', () => {
        const snapshot = [msg('2026-01-01T00:00:01Z', 'a'), msg('2026-01-01T00:00:02Z', 'b')];
        expect(mergeSnapshotWithLiveTail(snapshot, [])).toEqual(snapshot);
    });

    it('keeps live entries newer than the snapshot (DB persistence lag)', () => {
        const snapshot = [msg('2026-01-01T00:00:01Z', 'a')];
        const prev = [
            msg('2026-01-01T00:00:01Z', 'a'),
            msg('2026-01-01T00:00:02Z', 'live-tail'), // arrived over WS, not yet in DB
        ];
        expect(contents(mergeSnapshotWithLiveTail(snapshot, prev))).toEqual(['a', 'live-tail']);
    });

    it('deduplicates entries present in both snapshot and live stream by ts', () => {
        const shared = '2026-01-01T00:00:02Z';
        const snapshot = [msg('2026-01-01T00:00:01Z', 'a'), msg(shared, 'b')];
        const prev = [msg(shared, 'b'), msg('2026-01-01T00:00:03Z', 'c')];
        expect(contents(mergeSnapshotWithLiveTail(snapshot, prev))).toEqual(['a', 'b', 'c']);
    });

    it('handles same-millisecond live entries without dropping them', () => {
        // ts differs at sub-millisecond precision — Date.parse collapses them
        // to the same ms, so the tail filter must use >= (not >).
        const snapshot = [msg('2026-01-01T00:00:01.000100Z', 'a')];
        const prev = [msg('2026-01-01T00:00:01.000900Z', 'b')];
        expect(contents(mergeSnapshotWithLiveTail(snapshot, prev))).toEqual(['a', 'b']);
    });

    it('drops on-screen entries older than the snapshot tail that the DB does not have', () => {
        // e.g. leftovers from a rewritten transcript — the DB is the source
        // of truth for anything before its own last entry.
        const snapshot = [msg('2026-01-01T00:00:05Z', 'e')];
        const prev = [msg('2026-01-01T00:00:01Z', 'stale'), msg('2026-01-01T00:00:06Z', 'f')];
        expect(contents(mergeSnapshotWithLiveTail(snapshot, prev))).toEqual(['e', 'f']);
    });

    it('drops on-screen entries without a ts (log_content fallback rendering)', () => {
        const snapshot = [msg('2026-01-01T00:00:01Z', 'a')];
        const prev = [msg(undefined, 'no-ts'), msg('2026-01-01T00:00:02Z', 'b')];
        expect(contents(mergeSnapshotWithLiveTail(snapshot, prev))).toEqual(['a', 'b']);
    });

    it('preserves all live entries when the DB snapshot is still empty', () => {
        const prev = [msg('2026-01-01T00:00:01Z', 'x'), msg('2026-01-01T00:00:02Z', 'y')];
        expect(contents(mergeSnapshotWithLiveTail([], prev))).toEqual(['x', 'y']);
    });

    it('reassigns sequential ids so React keys stay unique after the merge', () => {
        const snapshot = [msg('2026-01-01T00:00:01Z', 'a', 5)];
        const prev = [msg('2026-01-01T00:00:02Z', 'b', 5)];
        const merged = mergeSnapshotWithLiveTail(snapshot, prev);
        expect(merged.map((m) => m.id)).toEqual([0, 1]);
    });
});
