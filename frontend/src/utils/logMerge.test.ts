import { describe, it, expect } from 'vitest';
import { mergeSnapshotWithLiveTail, sortBySeq, type LogMessage } from './logMerge';

const msg = (seq: number | undefined, content: string, id = 0): LogMessage => ({
    id,
    entry: seq === undefined ? { content } : { seq, content },
});

const contents = (msgs: LogMessage[]) => msgs.map((m) => m.entry.content);

describe('mergeSnapshotWithLiveTail', () => {
    it('returns the snapshot when nothing is on screen yet', () => {
        const snapshot = [msg(1, 'a'), msg(2, 'b')];
        expect(mergeSnapshotWithLiveTail(snapshot, [])).toEqual(snapshot);
    });

    it('keeps live entries beyond the snapshot (DB persistence lag)', () => {
        const snapshot = [msg(1, 'a')];
        const prev = [msg(1, 'a'), msg(2, 'live-tail')]; // seq 2 arrived over WS, not yet in DB
        expect(contents(mergeSnapshotWithLiveTail(snapshot, prev))).toEqual(['a', 'live-tail']);
    });

    it('deduplicates entries present in both snapshot and live stream', () => {
        const snapshot = [msg(1, 'a'), msg(2, 'b')];
        const prev = [msg(2, 'b'), msg(3, 'c')];
        expect(contents(mergeSnapshotWithLiveTail(snapshot, prev))).toEqual(['a', 'b', 'c']);
    });

    it('drops on-screen entries the snapshot superseded', () => {
        // e.g. leftovers from a rewritten transcript — the DB is the source
        // of truth for anything up to its own newest entry.
        const snapshot = [msg(5, 'e')];
        const prev = [msg(1, 'stale'), msg(6, 'f')];
        expect(contents(mergeSnapshotWithLiveTail(snapshot, prev))).toEqual(['e', 'f']);
    });

    it('drops on-screen entries without a seq (log_content fallback rendering)', () => {
        const snapshot = [msg(1, 'a')];
        const prev = [msg(undefined, 'no-seq'), msg(2, 'b')];
        expect(contents(mergeSnapshotWithLiveTail(snapshot, prev))).toEqual(['a', 'b']);
    });

    it('preserves all live entries when the DB snapshot is still empty', () => {
        const prev = [msg(1, 'x'), msg(2, 'y')];
        expect(contents(mergeSnapshotWithLiveTail([], prev))).toEqual(['x', 'y']);
    });

    it('reassigns sequential ids so React keys stay unique after the merge', () => {
        const merged = mergeSnapshotWithLiveTail([msg(1, 'a', 5)], [msg(2, 'b', 5)]);
        expect(merged.map((m) => m.id)).toEqual([0, 1]);
    });
});

describe('sortBySeq', () => {
    it('restores broadcast order for entries persisted out of order', () => {
        const shuffled = [msg(3, 'c'), msg(1, 'a'), msg(2, 'b')];
        expect(contents(sortBySeq(shuffled))).toEqual(['a', 'b', 'c']);
    });

    it('keeps stored order for pre-seq entries and does not mutate the input', () => {
        const legacy = [msg(undefined, 'old-1'), msg(undefined, 'old-2'), msg(1, 'new')];
        const sorted = sortBySeq(legacy);
        expect(contents(sorted)).toEqual(['old-1', 'old-2', 'new']);
        expect(contents(legacy)).toEqual(['old-1', 'old-2', 'new']);
    });
});
