export interface LogMessage {
    id: number;
    entry: { ts?: string } & Record<string, unknown>;
}

// mergeSnapshotWithLiveTail reconciles a freshly fetched DB snapshot with the
// entries currently on screen. Live WebSocket entries can be ahead of the
// database (persistence is async), and more can arrive while the fetch is in
// flight — so a plain replace would silently drop the newest lines until the
// next event or a manual reload. Entries share one `ts` between the broadcast
// and the persisted record, so ts is a reliable dedup key.
export function mergeSnapshotWithLiveTail(snapshot: LogMessage[], prev: LogMessage[]): LogMessage[] {
    if (prev.length === 0) return snapshot;
    const seen = new Set(snapshot.map((m) => m.entry?.ts).filter(Boolean));
    let lastMs = 0;
    for (const m of snapshot) {
        const t = m.entry?.ts ? Date.parse(m.entry.ts) : NaN;
        if (!isNaN(t) && t > lastMs) lastMs = t;
    }
    const tail = prev.filter((m) => {
        if (!m.entry?.ts || seen.has(m.entry.ts)) return false;
        const t = Date.parse(m.entry.ts);
        return !isNaN(t) && t >= lastMs;
    });
    return [...snapshot, ...tail].map((m, i) => ({ id: i, entry: m.entry }));
}
