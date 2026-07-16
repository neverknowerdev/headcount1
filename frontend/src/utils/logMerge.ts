export interface LogMessage {
    id: number;
    entry: { seq?: number } & Record<string, unknown>;
}

// The server stamps every run_log entry with a per-run monotonic `seq`,
// shared between the WebSocket broadcast and the persisted record. That makes
// reconciling a fresh DB snapshot with the live stream trivial: keep the
// on-screen entries the snapshot doesn't have yet (live delivery runs ahead
// of persistence), drop everything else as superseded by the snapshot.
export function mergeSnapshotWithLiveTail(snapshot: LogMessage[], prev: LogMessage[]): LogMessage[] {
    if (prev.length === 0) return snapshot;
    const lastSeq = Math.max(0, ...snapshot.map((m) => m.entry.seq ?? 0));
    const tail = prev.filter((m) => (m.entry.seq ?? 0) > lastSeq);
    return [...snapshot, ...tail].map((m, i) => ({ id: i, entry: m.entry }));
}

// sortBySeq orders snapshot entries by seq. Entries can be persisted slightly
// out of order when several producers write to one run (session logger plus
// per-request gateway loggers); seq restores the broadcast order. Entries
// without a seq (persisted before seq existed) keep their stored position.
export function sortBySeq(messages: LogMessage[]): LogMessage[] {
    return [...messages].sort((a, b) => (a.entry.seq ?? 0) - (b.entry.seq ?? 0));
}
