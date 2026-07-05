import React, { useState, useEffect, useCallback, useRef, useMemo } from 'react';
import axios from 'axios';
import {
    Brain, RefreshCw, Search, Trash2, Pencil, X, Sparkles, AlertTriangle,
} from 'lucide-react';
import { useStore } from '../store';

// ---------- Types ----------

interface MemoryBank {
    bank_id: string;
    kind: string;
    label: string;
}

interface GraphNode {
    id: string;
    label: string;
    type: string;
}

interface GraphEdge {
    from: string;
    to: string;
    type?: string;
    weight?: number;
}

interface GraphData {
    nodes: GraphNode[];
    edges: GraphEdge[];
    total_units?: number;
    limit?: number;
}

interface MemoryItem {
    id: string;
    text?: string;
    context?: string;
    date?: string;
    type?: string;
    [key: string]: any;
}

// ---------- Helpers ----------

const TYPE_COLORS: Record<string, string> = {
    world: '#6366f1',
    experience: '#10b981',
    observation: '#f59e0b',
    fact: '#0ea5e9',
    entity: '#8b5cf6',
    person: '#ec4899',
    organization: '#f97316',
    concept: '#14b8a6',
    location: '#84cc16',
    event: '#ef4444',
    document: '#64748b',
};

const colorForType = (type?: string): string => {
    if (!type) return '#94a3b8';
    if (TYPE_COLORS[type.toLowerCase()]) return TYPE_COLORS[type.toLowerCase()];
    // Deterministic fallback color from string hash
    let h = 0;
    for (let i = 0; i < type.length; i++) h = (h * 31 + type.charCodeAt(i)) >>> 0;
    const palette = ['#6366f1', '#10b981', '#f59e0b', '#0ea5e9', '#8b5cf6', '#ec4899', '#f97316', '#14b8a6'];
    return palette[h % palette.length];
};

const truncate = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + '…' : s);

const memText = (m: MemoryItem): string =>
    typeof m.text === 'string' ? m.text : (typeof (m as any).content === 'string' ? (m as any).content : JSON.stringify(m));

// ---------- Force-directed graph (self-contained, SVG) ----------

interface SimNode extends GraphNode {
    x: number;
    y: number;
    vx: number;
    vy: number;
}

const ForceGraph: React.FC<{
    data: GraphData;
    onNodeClick?: (node: GraphNode) => void;
}> = ({ data, onNodeClick }) => {
    const containerRef = useRef<HTMLDivElement>(null);
    const [size, setSize] = useState({ w: 800, h: 520 });
    const [nodes, setNodes] = useState<SimNode[]>([]);
    const [hover, setHover] = useState<{ node: SimNode; x: number; y: number } | null>(null);
    const animRef = useRef<number>(0);

    useEffect(() => {
        const el = containerRef.current;
        if (!el) return;
        const measure = () => setSize({ w: el.clientWidth || 800, h: 520 });
        measure();
        window.addEventListener('resize', measure);
        return () => window.removeEventListener('resize', measure);
    }, []);

    useEffect(() => {
        cancelAnimationFrame(animRef.current);
        const { w, h } = size;
        const n = data.nodes.length;
        if (n === 0) { setNodes([]); return; }

        // Init positions on a circle with jitter
        const sim: SimNode[] = data.nodes.map((node, i) => {
            const angle = (i / n) * Math.PI * 2;
            const r = Math.min(w, h) * 0.35;
            return {
                ...node,
                x: w / 2 + r * Math.cos(angle) + (Math.random() - 0.5) * 40,
                y: h / 2 + r * Math.sin(angle) + (Math.random() - 0.5) * 40,
                vx: 0,
                vy: 0,
            };
        });
        const index = new Map(sim.map((s) => [s.id, s]));
        const edges = data.edges
            .map((e) => ({ a: index.get(e.from), b: index.get(e.to), weight: e.weight || 1 }))
            .filter((e) => e.a && e.b) as { a: SimNode; b: SimNode; weight: number }[];

        let alpha = 1;
        const repulsion = 3000;
        const springLen = 90;
        const springK = 0.02;

        const tick = () => {
            // Repulsion (O(n^2), fine for a few hundred nodes)
            for (let i = 0; i < sim.length; i++) {
                for (let j = i + 1; j < sim.length; j++) {
                    const a = sim[i], b = sim[j];
                    let dx = a.x - b.x, dy = a.y - b.y;
                    let d2 = dx * dx + dy * dy;
                    if (d2 < 1) { dx = Math.random() - 0.5; dy = Math.random() - 0.5; d2 = 1; }
                    const f = (repulsion / d2) * alpha;
                    const d = Math.sqrt(d2);
                    a.vx += (dx / d) * f; a.vy += (dy / d) * f;
                    b.vx -= (dx / d) * f; b.vy -= (dy / d) * f;
                }
            }
            // Springs
            for (const e of edges) {
                const dx = e.b.x - e.a.x, dy = e.b.y - e.a.y;
                const d = Math.max(1, Math.sqrt(dx * dx + dy * dy));
                const f = springK * (d - springLen) * alpha * Math.min(e.weight, 3);
                e.a.vx += (dx / d) * f; e.a.vy += (dy / d) * f;
                e.b.vx -= (dx / d) * f; e.b.vy -= (dy / d) * f;
            }
            // Centering + integrate
            for (const s of sim) {
                s.vx += (w / 2 - s.x) * 0.005 * alpha;
                s.vy += (h / 2 - s.y) * 0.005 * alpha;
                s.vx *= 0.85; s.vy *= 0.85;
                s.x += s.vx; s.y += s.vy;
                s.x = Math.max(14, Math.min(w - 14, s.x));
                s.y = Math.max(14, Math.min(h - 14, s.y));
            }
            alpha *= 0.97;
            setNodes(sim.map((s) => ({ ...s })));
            if (alpha > 0.01) animRef.current = requestAnimationFrame(tick);
        };
        animRef.current = requestAnimationFrame(tick);
        return () => cancelAnimationFrame(animRef.current);
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [data, size.w, size.h]);

    const nodeIndex = useMemo(() => new Map(nodes.map((s) => [s.id, s])), [nodes]);
    const types = useMemo(() => Array.from(new Set(data.nodes.map((d) => d.type || 'unknown'))), [data]);

    return (
        <div ref={containerRef} className="relative w-full">
            <svg width={size.w} height={size.h} className="block bg-gray-50 rounded-lg border" data-testid="memory-graph">
                {data.edges.map((e, i) => {
                    const a = nodeIndex.get(e.from), b = nodeIndex.get(e.to);
                    if (!a || !b) return null;
                    return (
                        <line key={i} x1={a.x} y1={a.y} x2={b.x} y2={b.y}
                            stroke="#cbd5e1" strokeWidth={Math.min(1 + (e.weight || 1) * 0.4, 3)} strokeOpacity={0.6} />
                    );
                })}
                {nodes.map((s) => (
                    <g key={s.id}
                        transform={`translate(${s.x},${s.y})`}
                        className="cursor-pointer"
                        onMouseEnter={() => setHover({ node: s, x: s.x, y: s.y })}
                        onMouseLeave={() => setHover(null)}
                        onClick={() => onNodeClick && onNodeClick(s)}>
                        <circle r={7} fill={colorForType(s.type)} stroke="#fff" strokeWidth={1.5} />
                        <text x={10} y={4} fontSize={10} fill="#475569" className="select-none pointer-events-none">
                            {truncate(s.label || s.id, 24)}
                        </text>
                    </g>
                ))}
            </svg>
            {hover && (
                <div className="absolute z-10 max-w-xs bg-gray-900 text-white text-xs rounded-md px-3 py-2 shadow-lg pointer-events-none"
                    style={{ left: Math.min(hover.x + 12, size.w - 240), top: Math.max(hover.y - 10, 0) }}>
                    <div className="font-semibold mb-0.5">{truncate(hover.node.label || hover.node.id, 160)}</div>
                    <div className="text-gray-300">type: {hover.node.type || 'unknown'}</div>
                </div>
            )}
            <div className="flex flex-wrap gap-3 mt-2">
                {types.map((t) => (
                    <span key={t} className="flex items-center gap-1.5 text-xs text-gray-600">
                        <span className="w-2.5 h-2.5 rounded-full inline-block" style={{ backgroundColor: colorForType(t) }} />
                        {t}
                    </span>
                ))}
            </div>
        </div>
    );
};

// ---------- Detail panel ----------

const MemoryDetailPanel: React.FC<{
    bankId: string;
    memoryId: string;
    onClose: () => void;
    onChanged: () => void;
}> = ({ bankId, memoryId, onClose, onChanged }) => {
    const [memory, setMemory] = useState<MemoryItem | null>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);
    const [editing, setEditing] = useState(false);
    const [text, setText] = useState('');
    const [context, setContext] = useState('');
    const [saving, setSaving] = useState(false);

    useEffect(() => {
        let cancelled = false;
        setLoading(true);
        setError(null);
        setEditing(false);
        axios.get(`/api/memory/banks/${encodeURIComponent(bankId)}/memories/${encodeURIComponent(memoryId)}`)
            .then((res) => {
                if (cancelled) return;
                setMemory(res.data);
                setText(memText(res.data || {}));
                setContext(res.data?.context || '');
            })
            .catch((e) => { if (!cancelled) setError(e?.response?.data?.error || 'Failed to load memory'); })
            .finally(() => { if (!cancelled) setLoading(false); });
        return () => { cancelled = true; };
    }, [bankId, memoryId]);

    const save = async () => {
        setSaving(true);
        try {
            const res = await axios.patch(`/api/memory/banks/${encodeURIComponent(bankId)}/memories/${encodeURIComponent(memoryId)}`, { text, context });
            setMemory(res.data || { ...memory, text, context } as any);
            setEditing(false);
            onChanged();
        } catch (e: any) {
            alert(e?.response?.data?.error || 'Failed to update memory');
        } finally {
            setSaving(false);
        }
    };

    const remove = async () => {
        if (!confirm('Delete this memory? It will be invalidated in the memory bank.')) return;
        try {
            await axios.delete(`/api/memory/banks/${encodeURIComponent(bankId)}/memories/${encodeURIComponent(memoryId)}`);
            onChanged();
            onClose();
        } catch (e: any) {
            alert(e?.response?.data?.error || 'Failed to delete memory');
        }
    };

    return (
        <div className="w-96 flex-shrink-0 bg-white border rounded-lg shadow flex flex-col overflow-hidden" data-testid="memory-detail-panel">
            <div className="flex items-center justify-between px-4 py-3 border-b bg-gray-50">
                <h3 className="font-semibold text-sm text-gray-800">Memory details</h3>
                <div className="flex items-center gap-1">
                    {!loading && !error && !editing && (
                        <button onClick={() => setEditing(true)} title="Edit"
                            className="p-1.5 text-gray-500 hover:text-indigo-600 hover:bg-indigo-50 rounded">
                            <Pencil className="w-4 h-4" />
                        </button>
                    )}
                    {!loading && !error && (
                        <button onClick={remove} title="Delete"
                            className="p-1.5 text-gray-500 hover:text-red-600 hover:bg-red-50 rounded">
                            <Trash2 className="w-4 h-4" />
                        </button>
                    )}
                    <button onClick={onClose} title="Close" className="p-1.5 text-gray-500 hover:text-gray-800 hover:bg-gray-100 rounded">
                        <X className="w-4 h-4" />
                    </button>
                </div>
            </div>
            <div className="flex-1 overflow-y-auto p-4 text-sm">
                {loading ? (
                    <div className="text-gray-500 italic">Loading…</div>
                ) : error ? (
                    <div className="text-red-600">{error}</div>
                ) : editing ? (
                    <div className="space-y-3">
                        <div>
                            <label className="block text-xs font-medium text-gray-500 mb-1">Text</label>
                            <textarea value={text} onChange={(e) => setText(e.target.value)} rows={8}
                                className="w-full border rounded-md px-2 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500" />
                        </div>
                        <div>
                            <label className="block text-xs font-medium text-gray-500 mb-1">Context</label>
                            <textarea value={context} onChange={(e) => setContext(e.target.value)} rows={3}
                                className="w-full border rounded-md px-2 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500" />
                        </div>
                        <div className="flex gap-2">
                            <button onClick={save} disabled={saving}
                                className="px-3 py-1.5 bg-indigo-600 text-white rounded-md text-sm hover:bg-indigo-700 disabled:opacity-50">
                                {saving ? 'Saving…' : 'Save'}
                            </button>
                            <button onClick={() => setEditing(false)} className="px-3 py-1.5 border rounded-md text-sm hover:bg-gray-50">Cancel</button>
                        </div>
                    </div>
                ) : memory ? (
                    <div className="space-y-3">
                        <div className="flex flex-wrap gap-2 items-center">
                            {memory.type && (
                                <span className="text-xs px-2 py-0.5 rounded-full text-white" style={{ backgroundColor: colorForType(memory.type) }}>
                                    {memory.type}
                                </span>
                            )}
                            {memory.date && <span className="text-xs text-gray-500">{new Date(memory.date).toLocaleString()}</span>}
                        </div>
                        <div>
                            <div className="text-xs font-medium text-gray-500 mb-1">Text</div>
                            <div className="whitespace-pre-wrap text-gray-800">{memText(memory)}</div>
                        </div>
                        {memory.context && (
                            <div>
                                <div className="text-xs font-medium text-gray-500 mb-1">Context</div>
                                <div className="whitespace-pre-wrap text-gray-700">{memory.context}</div>
                            </div>
                        )}
                        {Array.isArray(memory.tags) && memory.tags.length > 0 && (
                            <div>
                                <div className="text-xs font-medium text-gray-500 mb-1">Tags</div>
                                <div className="flex flex-wrap gap-1">
                                    {memory.tags.map((t: string, i: number) => (
                                        <span key={i} className="text-xs bg-gray-100 text-gray-700 px-2 py-0.5 rounded-full">{t}</span>
                                    ))}
                                </div>
                            </div>
                        )}
                        {Array.isArray(memory.entities) && memory.entities.length > 0 && (
                            <div>
                                <div className="text-xs font-medium text-gray-500 mb-1">Entities</div>
                                <div className="flex flex-wrap gap-1">
                                    {memory.entities.map((e: any, i: number) => (
                                        <span key={i} className="text-xs bg-violet-50 text-violet-700 px-2 py-0.5 rounded-full">
                                            {typeof e === 'string' ? e : e?.name || e?.id || JSON.stringify(e)}
                                        </span>
                                    ))}
                                </div>
                            </div>
                        )}
                        <details className="text-xs text-gray-500">
                            <summary className="cursor-pointer hover:text-gray-700">Raw JSON</summary>
                            <pre className="mt-1 bg-gray-50 border rounded p-2 overflow-x-auto">{JSON.stringify(memory, null, 2)}</pre>
                        </details>
                    </div>
                ) : null}
            </div>
        </div>
    );
};

// ---------- Main page ----------

type Tab = 'graph' | 'memories' | 'query';

export const Memory: React.FC = () => {
    const { selectedCompanyId } = useStore();

    const [status, setStatus] = useState<{ available: boolean; url?: string } | null>(null);
    const [banks, setBanks] = useState<MemoryBank[]>([]);
    const [selectedBankId, setSelectedBankId] = useState<string>('');
    const [tab, setTab] = useState<Tab>('graph');
    const [selectedMemoryId, setSelectedMemoryId] = useState<string | null>(null);

    // Graph state
    const [graphMode, setGraphMode] = useState<'memories' | 'entities'>('memories');
    const [graph, setGraph] = useState<GraphData | null>(null);
    const [graphLoading, setGraphLoading] = useState(false);
    const [graphError, setGraphError] = useState<string | null>(null);

    // Memories list state
    const [memories, setMemories] = useState<MemoryItem[]>([]);
    const [memSearch, setMemSearch] = useState('');
    const [memType, setMemType] = useState('');
    const [memLoading, setMemLoading] = useState(false);
    const [memError, setMemError] = useState<string | null>(null);
    const [memRefreshKey, setMemRefreshKey] = useState(0);

    // Query panel state
    const [queryMode, setQueryMode] = useState<'recall' | 'ask'>('recall');
    const [query, setQuery] = useState('');
    const [queryLoading, setQueryLoading] = useState(false);
    const [queryError, setQueryError] = useState<string | null>(null);
    const [recallResults, setRecallResults] = useState<MemoryItem[]>([]);
    const [askAnswer, setAskAnswer] = useState<string | null>(null);

    // Sync state
    const [syncing, setSyncing] = useState(false);
    const [syncResult, setSyncResult] = useState<{ added: number; updated: number; removed: number } | null>(null);

    const available = status?.available === true;
    const selectedBank = banks.find((b) => b.bank_id === selectedBankId);

    // Poll status
    useEffect(() => {
        let cancelled = false;
        const check = async () => {
            try {
                const res = await axios.get('/api/memory/status');
                if (!cancelled) setStatus(res.data);
            } catch {
                if (!cancelled) setStatus({ available: false });
            }
        };
        check();
        const iv = setInterval(check, 10000);
        return () => { cancelled = true; clearInterval(iv); };
    }, []);

    // Load banks
    useEffect(() => {
        if (!selectedCompanyId || !available) return;
        let cancelled = false;
        axios.get(`/api/memory/banks?company_id=${selectedCompanyId}`)
            .then((res) => {
                if (cancelled) return;
                const list: MemoryBank[] = res.data || [];
                setBanks(list);
                setSelectedBankId((prev) => (prev && list.some((b) => b.bank_id === prev) ? prev : (list[0]?.bank_id || '')));
            })
            .catch(() => { if (!cancelled) setBanks([]); });
        return () => { cancelled = true; };
    }, [selectedCompanyId, available]);

    // Load graph
    useEffect(() => {
        if (!selectedBankId || !available || tab !== 'graph') return;
        let cancelled = false;
        setGraphLoading(true);
        setGraphError(null);
        const url = graphMode === 'memories'
            ? `/api/memory/banks/${encodeURIComponent(selectedBankId)}/graph?limit=150`
            : `/api/memory/banks/${encodeURIComponent(selectedBankId)}/entities-graph?limit=150`;
        axios.get(url)
            .then((res) => { if (!cancelled) setGraph({ nodes: res.data?.nodes || [], edges: res.data?.edges || [], total_units: res.data?.total_units, limit: res.data?.limit }); })
            .catch((e) => { if (!cancelled) { setGraph(null); setGraphError(e?.response?.data?.error || 'Failed to load graph'); } })
            .finally(() => { if (!cancelled) setGraphLoading(false); });
        return () => { cancelled = true; };
    }, [selectedBankId, available, graphMode, tab]);

    // Load memories list
    useEffect(() => {
        if (!selectedBankId || !available || tab !== 'memories') return;
        let cancelled = false;
        setMemLoading(true);
        setMemError(null);
        const params = new URLSearchParams({ limit: '100' });
        if (memSearch) params.set('q', memSearch);
        if (memType) params.set('type', memType);
        const t = setTimeout(() => {
            axios.get(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/memories?${params.toString()}`)
                .then((res) => {
                    if (cancelled) return;
                    const data = res.data;
                    const items = Array.isArray(data) ? data : (data?.items || data?.memories || data?.results || []);
                    setMemories(Array.isArray(items) ? items : []);
                })
                .catch((e) => { if (!cancelled) { setMemories([]); setMemError(e?.response?.data?.error || 'Failed to load memories'); } })
                .finally(() => { if (!cancelled) setMemLoading(false); });
        }, 300);
        return () => { cancelled = true; clearTimeout(t); };
    }, [selectedBankId, available, tab, memSearch, memType, memRefreshKey]);

    const memoryTypes = useMemo(() => Array.from(new Set(memories.map((m) => m.type).filter(Boolean))) as string[], [memories]);

    const runQuery = useCallback(async () => {
        if (!selectedBankId || !query.trim()) return;
        setQueryLoading(true);
        setQueryError(null);
        setRecallResults([]);
        setAskAnswer(null);
        try {
            if (queryMode === 'recall') {
                const res = await axios.post(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/recall`, { query: query.trim() });
                setRecallResults(res.data?.results || []);
            } else {
                const res = await axios.post(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/ask`, { query: query.trim() });
                setAskAnswer(res.data?.text || '');
            }
        } catch (e: any) {
            setQueryError(e?.response?.data?.error || 'Query failed');
        } finally {
            setQueryLoading(false);
        }
    }, [selectedBankId, query, queryMode]);

    const resyncDocs = useCallback(async () => {
        if (!selectedBank || !selectedBank.bank_id.startsWith('proj-')) return;
        const projectId = selectedBank.bank_id.slice('proj-'.length);
        setSyncing(true);
        setSyncResult(null);
        try {
            const res = await axios.post(`/api/memory/projects/${projectId}/sync`);
            setSyncResult(res.data);
        } catch (e: any) {
            alert(e?.response?.data?.error || 'Sync failed');
        } finally {
            setSyncing(false);
        }
    }, [selectedBank]);

    const onMemoryChanged = useCallback(() => setMemRefreshKey((k) => k + 1), []);

    const openMemory = useCallback((id: string) => setSelectedMemoryId(id), []);

    // ----- Render -----

    return (
        <div className="h-full flex flex-col">
            <div className="flex items-center justify-between mb-6 flex-wrap gap-3">
                <h1 className="text-2xl font-bold flex items-center gap-2">
                    <Brain className="w-7 h-7 text-indigo-600" /> Memory
                </h1>
                {available && banks.length > 0 && (
                    <div className="flex items-center gap-3 flex-wrap">
                        {syncResult && (
                            <span className="text-xs text-gray-600 bg-gray-100 border rounded-md px-2 py-1">
                                Synced: +{syncResult.added} added, {syncResult.updated} updated, −{syncResult.removed} removed
                            </span>
                        )}
                        {selectedBank?.bank_id.startsWith('proj-') && (
                            <button onClick={resyncDocs} disabled={syncing}
                                className="flex items-center gap-1.5 px-3 py-1.5 text-sm border rounded-md bg-white hover:bg-gray-50 text-gray-700 disabled:opacity-50">
                                <RefreshCw className={`w-4 h-4 ${syncing ? 'animate-spin' : ''}`} />
                                {syncing ? 'Syncing…' : 'Re-sync docs'}
                            </button>
                        )}
                        <select value={selectedBankId} onChange={(e) => { setSelectedBankId(e.target.value); setSelectedMemoryId(null); setSyncResult(null); }}
                            className="border rounded-md px-3 py-1.5 text-sm bg-white focus:outline-none focus:ring-2 focus:ring-indigo-500"
                            data-testid="memory-bank-select">
                            {banks.map((b) => (
                                <option key={b.bank_id} value={b.bank_id}>{b.label}</option>
                            ))}
                        </select>
                    </div>
                )}
            </div>

            {!available ? (
                <div className="flex-1 bg-white rounded-lg shadow border flex items-center justify-center">
                    <div className="text-center px-6 py-12 max-w-md">
                        <AlertTriangle className="w-10 h-10 text-amber-500 mx-auto mb-3" />
                        <h2 className="text-lg font-semibold text-gray-800 mb-1">Memory backend is starting or unavailable</h2>
                        <p className="text-sm text-gray-500">
                            The Hindsight memory service is not reachable right now. This page will refresh automatically once it becomes available.
                        </p>
                    </div>
                </div>
            ) : banks.length === 0 ? (
                <div className="flex-1 bg-white rounded-lg shadow border flex items-center justify-center">
                    <div className="text-gray-500 italic text-sm">No memory banks found for this company.</div>
                </div>
            ) : (
                <div className="flex-1 flex gap-4 min-h-0">
                    <div className="flex-1 flex flex-col min-w-0">
                        <div className="flex items-center gap-1 mb-3">
                            {([
                                ['graph', 'Graph'],
                                ['memories', 'Memories'],
                                ['query', 'Query'],
                            ] as [Tab, string][]).map(([t, label]) => (
                                <button key={t} onClick={() => setTab(t)}
                                    className={`px-3 py-1.5 text-sm font-medium rounded-md ${
                                        tab === t ? 'bg-indigo-50 text-indigo-600' : 'text-gray-600 hover:bg-gray-50 hover:text-gray-900'
                                    }`}>
                                    {label}
                                </button>
                            ))}
                        </div>

                        <div className="flex-1 bg-white p-4 rounded-lg shadow border overflow-y-auto min-h-0">
                            {tab === 'graph' && (
                                <div>
                                    <div className="flex items-center justify-between mb-3 flex-wrap gap-2">
                                        <div className="inline-flex rounded-md border overflow-hidden">
                                            <button onClick={() => setGraphMode('memories')}
                                                className={`px-3 py-1.5 text-sm ${graphMode === 'memories' ? 'bg-indigo-600 text-white' : 'bg-white text-gray-600 hover:bg-gray-50'}`}>
                                                Memories
                                            </button>
                                            <button onClick={() => setGraphMode('entities')}
                                                className={`px-3 py-1.5 text-sm border-l ${graphMode === 'entities' ? 'bg-indigo-600 text-white' : 'bg-white text-gray-600 hover:bg-gray-50'}`}>
                                                Entities
                                            </button>
                                        </div>
                                        {graph && graphMode === 'memories' && typeof graph.total_units === 'number' && (
                                            <span className="text-xs text-gray-500">
                                                Showing {graph.nodes.length} nodes{graph.total_units > (graph.limit || 0) ? ` of ${graph.total_units} memory units` : ''}
                                            </span>
                                        )}
                                    </div>
                                    {graphLoading ? (
                                        <div className="h-[520px] flex items-center justify-center text-gray-500 italic text-sm">Loading graph…</div>
                                    ) : graphError ? (
                                        <div className="h-[520px] flex items-center justify-center text-red-600 text-sm">{graphError}</div>
                                    ) : !graph || graph.nodes.length === 0 ? (
                                        <div className="h-[520px] flex items-center justify-center text-gray-500 italic text-sm">No graph data in this bank yet.</div>
                                    ) : (
                                        <ForceGraph data={graph}
                                            onNodeClick={(n) => { if (graphMode === 'memories') openMemory(n.id); }} />
                                    )}
                                </div>
                            )}

                            {tab === 'memories' && (
                                <div className="flex flex-col h-full">
                                    <div className="flex items-center gap-2 mb-3 flex-wrap">
                                        <div className="relative flex-1 min-w-[200px]">
                                            <Search className="w-4 h-4 text-gray-400 absolute left-2.5 top-1/2 -translate-y-1/2" />
                                            <input value={memSearch} onChange={(e) => setMemSearch(e.target.value)}
                                                placeholder="Search memories…"
                                                className="w-full border rounded-md pl-8 pr-3 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500" />
                                        </div>
                                        <select value={memType} onChange={(e) => setMemType(e.target.value)}
                                            className="border rounded-md px-2 py-1.5 text-sm bg-white focus:outline-none focus:ring-2 focus:ring-indigo-500">
                                            <option value="">All types</option>
                                            {memoryTypes.map((t) => <option key={t} value={t}>{t}</option>)}
                                        </select>
                                    </div>
                                    {memLoading ? (
                                        <div className="text-gray-500 italic text-sm py-8 text-center">Loading memories…</div>
                                    ) : memError ? (
                                        <div className="text-red-600 text-sm py-8 text-center">{memError}</div>
                                    ) : memories.length === 0 ? (
                                        <div className="text-gray-500 italic text-sm py-8 text-center">No memories found.</div>
                                    ) : (
                                        <div className="space-y-2">
                                            {memories.map((m) => (
                                                <button key={m.id} onClick={() => openMemory(m.id)}
                                                    className={`w-full text-left border rounded-lg px-3 py-2.5 hover:bg-gray-50 transition-colors ${
                                                        selectedMemoryId === m.id ? 'border-indigo-400 bg-indigo-50/50' : 'bg-white'
                                                    }`}>
                                                    <div className="flex items-center gap-2 mb-1">
                                                        {m.type && (
                                                            <span className="text-[10px] px-1.5 py-0.5 rounded-full text-white" style={{ backgroundColor: colorForType(m.type) }}>
                                                                {m.type}
                                                            </span>
                                                        )}
                                                        {m.date && <span className="text-xs text-gray-400">{new Date(m.date).toLocaleString()}</span>}
                                                    </div>
                                                    <div className="text-sm text-gray-800 line-clamp-2">{truncate(memText(m), 240)}</div>
                                                    {m.context && <div className="text-xs text-gray-500 mt-0.5 line-clamp-1">{truncate(m.context, 140)}</div>}
                                                </button>
                                            ))}
                                        </div>
                                    )}
                                </div>
                            )}

                            {tab === 'query' && (
                                <div>
                                    <div className="flex items-center gap-2 mb-3 flex-wrap">
                                        <div className="inline-flex rounded-md border overflow-hidden">
                                            <button onClick={() => setQueryMode('recall')}
                                                className={`px-3 py-1.5 text-sm ${queryMode === 'recall' ? 'bg-indigo-600 text-white' : 'bg-white text-gray-600 hover:bg-gray-50'}`}>
                                                Recall
                                            </button>
                                            <button onClick={() => setQueryMode('ask')}
                                                className={`px-3 py-1.5 text-sm border-l ${queryMode === 'ask' ? 'bg-indigo-600 text-white' : 'bg-white text-gray-600 hover:bg-gray-50'}`}>
                                                Ask memory
                                            </button>
                                        </div>
                                    </div>
                                    <div className="flex gap-2 mb-4">
                                        <input value={query} onChange={(e) => setQuery(e.target.value)}
                                            onKeyDown={(e) => { if (e.key === 'Enter') runQuery(); }}
                                            placeholder={queryMode === 'recall' ? 'Search memories semantically…' : 'Ask a question about this memory bank…'}
                                            className="flex-1 border rounded-md px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500" />
                                        <button onClick={runQuery} disabled={queryLoading || !query.trim()}
                                            className="flex items-center gap-1.5 px-4 py-2 bg-indigo-600 text-white rounded-md text-sm hover:bg-indigo-700 disabled:opacity-50">
                                            {queryMode === 'ask' ? <Sparkles className="w-4 h-4" /> : <Search className="w-4 h-4" />}
                                            {queryLoading ? 'Running…' : (queryMode === 'ask' ? 'Ask' : 'Recall')}
                                        </button>
                                    </div>
                                    {queryError && <div className="text-red-600 text-sm mb-3">{queryError}</div>}
                                    {queryMode === 'recall' ? (
                                        recallResults.length === 0 ? (
                                            !queryLoading && <div className="text-gray-500 italic text-sm">Run a recall query to see matching memories.</div>
                                        ) : (
                                            <div className="space-y-2">
                                                {recallResults.map((m, i) => (
                                                    <button key={m.id || i} onClick={() => m.id && openMemory(m.id)}
                                                        className="w-full text-left border rounded-lg px-3 py-2.5 bg-white hover:bg-gray-50 transition-colors">
                                                        <div className="flex items-center gap-2 mb-1">
                                                            {m.type && (
                                                                <span className="text-[10px] px-1.5 py-0.5 rounded-full text-white" style={{ backgroundColor: colorForType(m.type) }}>
                                                                    {m.type}
                                                                </span>
                                                            )}
                                                        </div>
                                                        <div className="text-sm text-gray-800">{memText(m)}</div>
                                                        {m.context && <div className="text-xs text-gray-500 mt-0.5">{truncate(m.context, 200)}</div>}
                                                    </button>
                                                ))}
                                            </div>
                                        )
                                    ) : (
                                        askAnswer !== null ? (
                                            <div className="bg-gray-50 border rounded-lg p-4 text-sm text-gray-800 whitespace-pre-wrap leading-relaxed">
                                                {askAnswer || <span className="italic text-gray-500">No answer returned.</span>}
                                            </div>
                                        ) : (
                                            !queryLoading && <div className="text-gray-500 italic text-sm">Ask a question and the memory layer will reason over the bank to answer.</div>
                                        )
                                    )}
                                </div>
                            )}
                        </div>
                    </div>

                    {selectedMemoryId && selectedBankId && (
                        <MemoryDetailPanel
                            bankId={selectedBankId}
                            memoryId={selectedMemoryId}
                            onClose={() => setSelectedMemoryId(null)}
                            onChanged={onMemoryChanged}
                        />
                    )}
                </div>
            )}
        </div>
    );
};
