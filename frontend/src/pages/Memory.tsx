import React, { useState, useEffect, useCallback, useRef, useMemo } from 'react';
import axios from 'axios';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import {
    Brain, RefreshCw, Search, Trash2, Pencil, X, Sparkles, AlertTriangle, Plus, History, ChevronDown, ChevronUp,
} from 'lucide-react';
import { useStore } from '../store';

// ---------- Types ----------

interface MemoryBank {
    bank_id: string;
    kind: string;
    label: string;
}

interface Project {
    id: number;
    name: string;
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

// Extracts the server's error message from a failed request, if present.
const errMsg = (e: unknown, fallback: string): string => {
    if (axios.isAxiosError(e)) {
        const data = e.response?.data as { error?: string } | undefined;
        if (data?.error) return data.error;
    }
    return fallback;
};

// Shared wrapper classes for rendered markdown (same as RunLogViewer/TaskModal).
const MD_CLASSES = 'prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0';

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
            .catch((e: unknown) => { if (!cancelled) setError(errMsg(e, 'Failed to load memory')); })
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
        } catch (e) {
            alert(errMsg(e, 'Failed to update memory'));
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
        } catch (e) {
            alert(errMsg(e, 'Failed to delete memory'));
        }
    };

    return (
        <div className="w-96 flex-shrink-0 bg-white border rounded-lg shadow flex flex-col overflow-hidden" data-testid="memory-detail-panel">
            <div className="flex items-center justify-between px-4 py-3 border-b bg-gray-50">
                <h3 className="font-semibold text-sm text-gray-800">Memory details</h3>
                <div className="flex items-center gap-1">
                    {!loading && !error && !editing && (
                        <button onClick={() => setEditing(true)} title="Edit" aria-label="Edit memory"
                            className="p-1.5 text-gray-500 hover:text-indigo-600 hover:bg-indigo-50 rounded">
                            <Pencil className="w-4 h-4" />
                        </button>
                    )}
                    {!loading && !error && (
                        <button onClick={remove} title="Delete" aria-label="Delete memory"
                            className="p-1.5 text-gray-500 hover:text-red-600 hover:bg-red-50 rounded">
                            <Trash2 className="w-4 h-4" />
                        </button>
                    )}
                    <button onClick={onClose} title="Close" aria-label="Close details" className="p-1.5 text-gray-500 hover:text-gray-800 hover:bg-gray-100 rounded">
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

type Tab = 'graph' | 'memories' | 'query' | 'insights';

interface MentalModel {
    id: string;
    name: string;
    source_query?: string;
    content?: string | null;
    last_refreshed_at?: string | null;
    is_stale?: boolean;
    tags?: string[];
    max_tokens?: number;
    trigger?: { refresh_after_consolidation?: boolean };
}

interface HistoryEntry {
    content: string;
    refreshed_at: string;
}

interface AgentConfig {
    name: string;
    [key: string]: any;
}

const MODEL_ID_RE = /^[a-z0-9-]+$/;

// Must mirror pkg/hindsight/service.go agentTag exactly:
// "agent:" + strings.ToLower(strings.ReplaceAll(role, " ", "-"))
const agentTagFor = (name: string) => `agent:${name.toLowerCase().replace(/ /g, '-')}`;

const clampTokens = (v: string): number => Math.max(256, Math.min(8192, Number(v) || 2048));

// ---------- Mental model create/edit form ----------

const MentalModelForm: React.FC<{
    bankId: string;
    model: MentalModel | null; // null = create
    agentConfigs: AgentConfig[];
    projects: Project[];
    bankTags: string[];
    onClose: () => void;
    onSaved: () => void;
}> = ({ bankId, model, agentConfigs, projects, bankTags, onClose, onSaved }) => {
    const isEdit = !!model;
    const [id, setId] = useState(model?.id || '');
    const [name, setName] = useState(model?.name || '');
    const [sourceQuery, setSourceQuery] = useState(model?.source_query || '');
    // Kept as a raw string so typing isn't clamped mid-keystroke; clamped on blur/submit.
    const [maxTokens, setMaxTokens] = useState(String(model?.max_tokens || 2048));
    const [autoRefresh, setAutoRefresh] = useState(model?.trigger?.refresh_after_consolidation !== false);
    const [tags, setTags] = useState<string[]>(model?.tags || []);
    const [tagInput, setTagInput] = useState('');
    const [showSuggestions, setShowSuggestions] = useState(false);
    const [saving, setSaving] = useState(false);
    const [error, setError] = useState<string | null>(null);

    const idError = id.trim() && !MODEL_ID_RE.test(id.trim())
        ? 'ID must contain only lowercase letters, numbers, and hyphens.'
        : null;

    const suggestions = useMemo(() => {
        const set = new Set<string>();
        const list: { tag: string; label: string }[] = [];
        const add = (tag: string, label: string) => {
            if (set.has(tag) || tags.includes(tag)) return;
            set.add(tag);
            list.push({ tag, label });
        };
        bankTags.forEach((t) => add(t, t));
        agentConfigs.forEach((a) => { const t = agentTagFor(a.name); add(t, `${t} (${a.name})`); });
        projects.forEach((p) => { const t = `project:${p.id}`; add(t, `${t} (${p.name})`); });
        const q = tagInput.trim().toLowerCase();
        return (q ? list.filter((s) => s.tag.toLowerCase().includes(q) || s.label.toLowerCase().includes(q)) : list).slice(0, 30);
    }, [bankTags, agentConfigs, projects, tagInput, tags]);

    const addTag = (tag: string) => {
        const t = tag.trim();
        if (!t || tags.includes(t)) return;
        setTags((prev) => [...prev, t]);
        setTagInput('');
        setShowSuggestions(false);
    };

    const removeTag = (tag: string) => setTags((prev) => prev.filter((t) => t !== tag));

    const submit = async () => {
        if (!name.trim() || !sourceQuery.trim() || idError) return;
        setSaving(true);
        setError(null);
        try {
            const body: any = {
                name: name.trim(),
                source_query: sourceQuery.trim(),
                tags,
                max_tokens: clampTokens(maxTokens),
                trigger: { refresh_after_consolidation: autoRefresh },
            };
            if (isEdit && model) {
                await axios.patch(`/api/memory/banks/${encodeURIComponent(bankId)}/mental-models/${encodeURIComponent(model.id)}`, body);
            } else {
                if (id.trim()) body.id = id.trim();
                await axios.post(`/api/memory/banks/${encodeURIComponent(bankId)}/mental-models`, body);
            }
            onSaved();
        } catch (e) {
            setError(errMsg(e, 'Failed to save mental model'));
        } finally {
            setSaving(false);
        }
    };

    return (
        <div className="fixed inset-0 bg-black/30 flex items-center justify-center z-50" onClick={onClose}>
            <div className="w-full max-w-lg bg-white rounded-lg shadow border max-h-[90vh] overflow-y-auto"
                onClick={(e) => e.stopPropagation()} data-testid="mental-model-form">
                <div className="flex items-center justify-between px-4 py-3 border-b bg-gray-50">
                    <h3 className="font-semibold text-sm text-gray-800">{isEdit ? 'Edit mental model' : 'New mental model'}</h3>
                    <button onClick={onClose} title="Close" aria-label="Close form" className="p-1.5 text-gray-500 hover:text-gray-800 hover:bg-gray-100 rounded">
                        <X className="w-4 h-4" />
                    </button>
                </div>
                <div className="p-4 space-y-3 text-sm">
                    {error && <div className="text-red-600 text-sm">{error}</div>}
                    <div>
                        <label className="block text-xs font-medium text-gray-500 mb-1">Name</label>
                        <input value={name} onChange={(e) => setName(e.target.value)} required
                            className="w-full border rounded-md px-2 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500" />
                    </div>
                    <div>
                        <label className="block text-xs font-medium text-gray-500 mb-1">ID</label>
                        <input value={id} onChange={(e) => setId(e.target.value)} disabled={isEdit}
                            placeholder="auto-generated if left blank"
                            className="w-full border rounded-md px-2 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500 disabled:bg-gray-100 disabled:text-gray-500" />
                        {idError && <div className="text-xs text-red-600 mt-1">{idError}</div>}
                    </div>
                    <div>
                        <label className="block text-xs font-medium text-gray-500 mb-1">Source query</label>
                        <textarea value={sourceQuery} onChange={(e) => setSourceQuery(e.target.value)} rows={3} required
                            placeholder="e.g. What is the current implementation state of this project?"
                            className="w-full border rounded-md px-2 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500" />
                    </div>
                    <div>
                        <label className="block text-xs font-medium text-gray-500 mb-1">Max tokens</label>
                        <input type="number" min={256} max={8192} value={maxTokens}
                            onChange={(e) => setMaxTokens(e.target.value)}
                            onBlur={() => setMaxTokens(String(clampTokens(maxTokens)))}
                            className="w-full border rounded-md px-2 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500" />
                    </div>
                    <label className="flex items-center gap-2 text-sm text-gray-700">
                        <input type="checkbox" checked={autoRefresh} onChange={(e) => setAutoRefresh(e.target.checked)} />
                        Auto-refresh after new memories
                    </label>
                    <div>
                        <label className="block text-xs font-medium text-gray-500 mb-1">Tags</label>
                        <div className="flex flex-wrap gap-1.5 mb-1.5">
                            {tags.map((t) => (
                                <span key={t} className="flex items-center gap-1 text-xs bg-indigo-50 text-indigo-700 px-2 py-0.5 rounded-full">
                                    {t}
                                    <button onClick={() => removeTag(t)} aria-label={`Remove tag ${t}`} className="hover:text-indigo-900">
                                        <X className="w-3 h-3" />
                                    </button>
                                </span>
                            ))}
                        </div>
                        <div className="relative">
                            <input value={tagInput}
                                onChange={(e) => { setTagInput(e.target.value); setShowSuggestions(true); }}
                                onFocus={() => setShowSuggestions(true)}
                                onBlur={() => {
                                    // Delay so clicking a suggestion (which blurs first) still registers.
                                    window.setTimeout(() => setShowSuggestions(false), 150);
                                }}
                                onKeyDown={(e) => {
                                    if (e.key === 'Enter') { e.preventDefault(); addTag(tagInput); }
                                }}
                                placeholder="Type to search or add a tag…"
                                className="w-full border rounded-md px-2 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500" />
                            {showSuggestions && (suggestions.length > 0 || tagInput.trim()) && (
                                <div className="absolute z-10 mt-1 w-full max-h-48 overflow-y-auto bg-white border rounded-md shadow"
                                    /* keep input focused so blur doesn't race the click */
                                    onMouseDown={(e) => e.preventDefault()}>
                                    {suggestions.map((s) => (
                                        <button key={s.tag} type="button" onClick={() => addTag(s.tag)}
                                            className="block w-full text-left px-2 py-1.5 text-xs hover:bg-indigo-50 text-gray-700">
                                            {s.label}
                                        </button>
                                    ))}
                                    {tagInput.trim() && !suggestions.some((s) => s.tag === tagInput.trim()) && (
                                        <button type="button" onClick={() => addTag(tagInput)}
                                            className="block w-full text-left px-2 py-1.5 text-xs hover:bg-indigo-50 text-indigo-700 border-t">
                                            Add "{tagInput.trim()}"
                                        </button>
                                    )}
                                </div>
                            )}
                        </div>
                    </div>
                    <div className="flex gap-2 pt-1">
                        <button onClick={submit} disabled={saving || !name.trim() || !sourceQuery.trim() || !!idError}
                            className="px-3 py-1.5 bg-indigo-600 text-white rounded-md text-sm hover:bg-indigo-700 disabled:opacity-50">
                            {saving ? 'Saving…' : (isEdit ? 'Save changes' : 'Create')}
                        </button>
                        <button onClick={onClose} className="px-3 py-1.5 border rounded-md text-sm hover:bg-gray-50">Cancel</button>
                    </div>
                </div>
            </div>
        </div>
    );
};

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

    // Insights (mental models) state
    const [models, setModels] = useState<MentalModel[]>([]);
    const [modelsLoading, setModelsLoading] = useState(false);
    const [modelsError, setModelsError] = useState<string | null>(null);
    const [modelActionId, setModelActionId] = useState<string | null>(null);
    const [bankTags, setBankTags] = useState<string[]>([]);
    const [agentConfigs, setAgentConfigs] = useState<AgentConfig[]>([]);
    const [showModelForm, setShowModelForm] = useState(false);
    const [editingModel, setEditingModel] = useState<MentalModel | null>(null);
    const [editingModelLoading, setEditingModelLoading] = useState(false);
    const [expandedHistoryId, setExpandedHistoryId] = useState<string | null>(null);
    const [historyByModel, setHistoryByModel] = useState<Record<string, HistoryEntry[]>>({});
    const [historyLoadingId, setHistoryLoadingId] = useState<string | null>(null);
    const [historyError, setHistoryError] = useState<Record<string, string>>({});
    const [expandedHistoryEntries, setExpandedHistoryEntries] = useState<Record<string, boolean>>({});
    const [modelsRefreshKey, setModelsRefreshKey] = useState(0);
    // Model refresh is async server-side (202 Accepted): remember each queued
    // model's last_refreshed_at at queue time so the card can show a pending
    // indicator until the value changes on a subsequent list fetch.
    const [queuedRefresh, setQueuedRefresh] = useState<Record<string, string>>({});
    const refreshTimersRef = useRef<number[]>([]);
    useEffect(() => () => { refreshTimersRef.current.forEach((t) => window.clearTimeout(t)); }, []);

    // Sync state — with one shared bank per company, doc re-sync targets a
    // specific project rather than the (now singular) bank.
    const [projects, setProjects] = useState<Project[]>([]);
    const [syncProjectId, setSyncProjectId] = useState<string>('');
    const [syncing, setSyncing] = useState(false);
    const [syncResult, setSyncResult] = useState<{ added: number; updated: number; removed: number } | null>(null);

    const available = status?.available === true;

    // Reset per-bank UI state whenever the selected bank changes (including
    // programmatic changes on company switch, which don't go through the
    // bank <select>'s onChange).
    useEffect(() => {
        setSelectedMemoryId(null);
        setRecallResults([]);
        setAskAnswer(null);
        setQueryError(null);
        setSyncResult(null);
        setGraph(null);
        setQueuedRefresh({});
    }, [selectedBankId]);

    // History snapshots are per-bank and can change on any model
    // refresh/save/delete, so drop the cache whenever either changes.
    useEffect(() => {
        setHistoryByModel({});
        setHistoryError({});
        setExpandedHistoryEntries({});
        setHistoryLoadingId(null);
        setExpandedHistoryId(null);
    }, [selectedBankId, modelsRefreshKey]);

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

    // Load the company's projects — used only to pick which project's docs
    // to re-sync (project docs and agent run experience now share one bank
    // per company, so there is no per-project bank to infer this from).
    useEffect(() => {
        if (!selectedCompanyId) return;
        let cancelled = false;
        axios.get(`/api/projects?company_id=${selectedCompanyId}`)
            .then((res) => {
                if (cancelled) return;
                const list: Project[] = res.data || [];
                setProjects(list);
                setSyncProjectId((prev) => (prev && list.some((p) => String(p.id) === prev) ? prev : String(list[0]?.id || '')));
            })
            .catch(() => { if (!cancelled) setProjects([]); });
        return () => { cancelled = true; };
    }, [selectedCompanyId]);

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
            .catch((e: unknown) => { if (!cancelled) { setGraph(null); setGraphError(errMsg(e, 'Failed to load graph')); } })
            .finally(() => { if (!cancelled) setGraphLoading(false); });
        return () => { cancelled = true; };
        // memRefreshKey: re-fetch after a memory is edited/deleted so the graph doesn't show stale nodes.
    }, [selectedBankId, available, graphMode, tab, memRefreshKey]);

    // Load memories list
    useEffect(() => {
        if (!selectedBankId || !available || tab !== 'memories') return;
        let cancelled = false;
        setMemLoading(true);
        setMemError(null);
        // state=active pins the backend's list behavior: soft-deleted
        // (invalidated) memories must not reappear after a delete + refetch.
        const params = new URLSearchParams({ limit: '100', state: 'active' });
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
                .catch((e: unknown) => { if (!cancelled) { setMemories([]); setMemError(errMsg(e, 'Failed to load memories')); } })
                .finally(() => { if (!cancelled) setMemLoading(false); });
        }, 300);
        return () => { cancelled = true; clearTimeout(t); };
    }, [selectedBankId, available, tab, memSearch, memType, memRefreshKey]);

    // Load mental models (Insights tab) — synthesized, auto-refreshing
    // knowledge (project state, agent playbooks, open blockers) rather than
    // individual memories.
    useEffect(() => {
        if (!selectedBankId || !available || tab !== 'insights') return;
        let cancelled = false;
        setModelsLoading(true);
        setModelsError(null);
        axios.get(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/mental-models`)
            .then((res) => {
                if (cancelled) return;
                const data = res.data;
                const items = Array.isArray(data) ? data : (data?.items || data?.mental_models || []);
                const list: MentalModel[] = Array.isArray(items) ? items : [];
                setModels(list);
                // Drop "refresh queued" markers for models whose content has
                // since been regenerated (last_refreshed_at moved on).
                setQueuedRefresh((prev) => {
                    const next: Record<string, string> = {};
                    for (const [id, at] of Object.entries(prev)) {
                        const m = list.find((x) => x.id === id);
                        if (m && (m.last_refreshed_at || '') === at) next[id] = at;
                    }
                    return next;
                });
            })
            .catch((e: unknown) => { if (!cancelled) { setModels([]); setModelsError(errMsg(e, 'Failed to load insights')); } })
            .finally(() => { if (!cancelled) setModelsLoading(false); });
        return () => { cancelled = true; };
    }, [selectedBankId, available, tab, modelsRefreshKey]);

    // Load agent configs (once) — used to suggest agent:<role> tags.
    useEffect(() => {
        let cancelled = false;
        axios.get('/api/agent-configs')
            .then((res) => { if (!cancelled) setAgentConfigs(Array.isArray(res.data) ? res.data : []); })
            .catch(() => { if (!cancelled) setAgentConfigs([]); });
        return () => { cancelled = true; };
    }, []);

    // Load the bank's already-observed tags — used to suggest tags in the form.
    useEffect(() => {
        if (!selectedBankId || !available || tab !== 'insights') return;
        let cancelled = false;
        axios.get(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/tags`)
            .then((res) => {
                if (cancelled) return;
                const items = res.data?.items || [];
                setBankTags(Array.isArray(items) ? items.map((i: any) => i.tag).filter(Boolean) : []);
            })
            .catch(() => { if (!cancelled) setBankTags([]); });
        return () => { cancelled = true; };
    }, [selectedBankId, available, tab, modelsRefreshKey]);

    const refreshModel = useCallback(async (id: string) => {
        if (!selectedBankId) return;
        setModelActionId(id);
        try {
            // The server responds 202 Accepted: regeneration happens in the
            // background. Mark the model as queued and refetch the list a
            // couple of times to pick up the new content.
            await axios.post(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/mental-models/${encodeURIComponent(id)}/refresh`);
            const current = models.find((m) => m.id === id);
            setQueuedRefresh((prev) => ({ ...prev, [id]: current?.last_refreshed_at || '' }));
            for (const delay of [4000, 12000]) {
                refreshTimersRef.current.push(window.setTimeout(() => setModelsRefreshKey((k) => k + 1), delay));
            }
        } catch (e) {
            alert(errMsg(e, 'Refresh failed'));
        } finally {
            setModelActionId(null);
        }
    }, [selectedBankId, models]);

    const deleteModel = useCallback(async (id: string) => {
        if (!selectedBankId || !confirm(`Delete insight "${id}"? It will be re-created automatically the next time it is needed.`)) return;
        setModelActionId(id);
        try {
            await axios.delete(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/mental-models/${encodeURIComponent(id)}`);
            setModelsRefreshKey((k) => k + 1);
        } catch (e) {
            alert(errMsg(e, 'Delete failed'));
        } finally {
            setModelActionId(null);
        }
    }, [selectedBankId]);

    const openCreateModel = useCallback(() => {
        setEditingModel(null);
        setShowModelForm(true);
    }, []);

    const openEditModel = useCallback(async (m: MentalModel) => {
        if (!selectedBankId) return;
        setEditingModelLoading(true);
        try {
            const res = await axios.get(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/mental-models/${encodeURIComponent(m.id)}`);
            setEditingModel(res.data || m);
            setShowModelForm(true);
        } catch (e) {
            alert(errMsg(e, 'Failed to load mental model'));
        } finally {
            setEditingModelLoading(false);
        }
    }, [selectedBankId]);

    const onModelFormSaved = useCallback(() => {
        setShowModelForm(false);
        setEditingModel(null);
        setModelsRefreshKey((k) => k + 1);
    }, []);

    const toggleHistory = useCallback((id: string) => {
        setExpandedHistoryId((prev) => (prev === id ? null : id));
        if (expandedHistoryId !== id && !historyByModel[id] && selectedBankId) {
            setHistoryLoadingId(id);
            setHistoryError((prev) => ({ ...prev, [id]: '' }));
            axios.get(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/mental-models/${encodeURIComponent(id)}/history`)
                .then((res) => {
                    const items = res.data?.items || [];
                    setHistoryByModel((prev) => ({ ...prev, [id]: Array.isArray(items) ? items : [] }));
                })
                .catch((e: unknown) => {
                    setHistoryError((prev) => ({ ...prev, [id]: errMsg(e, 'Failed to load history') }));
                })
                .finally(() => setHistoryLoadingId((prev) => (prev === id ? null : prev)));
        }
    }, [expandedHistoryId, historyByModel, selectedBankId]);

    const memoryTypes = useMemo(() => Array.from(new Set(memories.map((m) => m.type).filter(Boolean))) as string[], [memories]);

    const querySeqRef = useRef(0);
    const runQuery = useCallback(async () => {
        if (!selectedBankId || !query.trim() || queryLoading) return;
        // Sequence counter: if another query starts before this one resolves,
        // the stale response must not overwrite the newer one's state.
        const seq = ++querySeqRef.current;
        setQueryLoading(true);
        setQueryError(null);
        setRecallResults([]);
        setAskAnswer(null);
        try {
            if (queryMode === 'recall') {
                const res = await axios.post(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/recall`, { query: query.trim() });
                if (seq === querySeqRef.current) setRecallResults(res.data?.results || []);
            } else {
                const res = await axios.post(`/api/memory/banks/${encodeURIComponent(selectedBankId)}/ask`, { query: query.trim() });
                if (seq === querySeqRef.current) setAskAnswer(res.data?.text || '');
            }
        } catch (e) {
            if (seq === querySeqRef.current) setQueryError(errMsg(e, 'Query failed'));
        } finally {
            if (seq === querySeqRef.current) setQueryLoading(false);
        }
    }, [selectedBankId, query, queryMode, queryLoading]);

    const resyncDocs = useCallback(async () => {
        if (!syncProjectId) return;
        setSyncing(true);
        setSyncResult(null);
        try {
            const res = await axios.post(`/api/memory/projects/${syncProjectId}/sync`);
            setSyncResult(res.data);
        } catch (e) {
            alert(errMsg(e, 'Sync failed'));
        } finally {
            setSyncing(false);
        }
    }, [syncProjectId]);

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
                        {projects.length > 0 && (
                            <>
                                <select value={syncProjectId} onChange={(e) => { setSyncProjectId(e.target.value); setSyncResult(null); }}
                                    className="border rounded-md px-3 py-1.5 text-sm bg-white focus:outline-none focus:ring-2 focus:ring-indigo-500"
                                    data-testid="memory-sync-project-select">
                                    {projects.map((p) => (
                                        <option key={p.id} value={p.id}>{p.name}</option>
                                    ))}
                                </select>
                                <button onClick={resyncDocs} disabled={syncing || !syncProjectId}
                                    className="flex items-center gap-1.5 px-3 py-1.5 text-sm border rounded-md bg-white hover:bg-gray-50 text-gray-700 disabled:opacity-50">
                                    <RefreshCw className={`w-4 h-4 ${syncing ? 'animate-spin' : ''}`} />
                                    {syncing ? 'Syncing…' : 'Re-sync docs'}
                                </button>
                            </>
                        )}
                        {/* Per-bank state (detail panel, query results, …) is reset by the selectedBankId effect above. */}
                        <select value={selectedBankId} onChange={(e) => setSelectedBankId(e.target.value)}
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
                                ['insights', 'Insights'],
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
                                            <div className="bg-gray-50 border rounded-lg p-4 text-sm text-gray-800 leading-relaxed">
                                                {askAnswer ? (
                                                    <div className={MD_CLASSES}>
                                                        <ReactMarkdown remarkPlugins={[remarkGfm]}>{askAnswer}</ReactMarkdown>
                                                    </div>
                                                ) : <span className="italic text-gray-500">No answer returned.</span>}
                                            </div>
                                        ) : (
                                            !queryLoading && <div className="text-gray-500 italic text-sm">Ask a question and the memory layer will reason over the bank to answer.</div>
                                        )
                                    )}
                                </div>
                            )}

                            {tab === 'insights' && (
                                <div>
                                    <div className="flex items-start justify-between gap-3 mb-3">
                                        <p className="text-xs text-gray-500">
                                            Synthesized understanding the memory layer keeps up to date in the background —
                                            project state, agent playbooks, open blockers — fetched instantly rather than
                                            recomputed per request.
                                        </p>
                                        <button onClick={openCreateModel}
                                            className="flex items-center gap-1.5 px-3 py-1.5 text-sm bg-indigo-600 text-white rounded-md hover:bg-indigo-700 shrink-0">
                                            <Plus className="w-4 h-4" /> New mental model
                                        </button>
                                    </div>
                                    {modelsError && <div className="text-red-600 text-sm mb-3">{modelsError}</div>}
                                    {modelsLoading && models.length === 0 ? (
                                        <div className="text-gray-500 italic text-sm">Loading…</div>
                                    ) : models.length === 0 ? (
                                        <div className="text-gray-500 italic text-sm">
                                            No insights yet — they appear once a project or agent has enough memory to synthesize from.
                                        </div>
                                    ) : (
                                        <div className="space-y-3">
                                            {models.map((m) => (
                                                <div key={m.id} className="border rounded-lg p-3 bg-white">
                                                    <div className="flex items-start justify-between gap-2 mb-1.5">
                                                        <div>
                                                            <div className="font-medium text-sm text-gray-900 flex items-center gap-2">
                                                                {m.name || m.id}
                                                                {queuedRefresh[m.id] !== undefined && (
                                                                    <span className="flex items-center gap-1 text-[11px] font-normal text-indigo-600">
                                                                        <RefreshCw className="w-3 h-3 animate-spin" /> Refresh queued…
                                                                    </span>
                                                                )}
                                                            </div>
                                                            {m.source_query && <div className="text-xs text-gray-500 mt-0.5">{m.source_query}</div>}
                                                        </div>
                                                        <div className="flex items-center gap-1.5 shrink-0">
                                                            <button onClick={() => toggleHistory(m.id)}
                                                                title="View history" aria-label={`View history of ${m.name || m.id}`}
                                                                className="p-1.5 rounded hover:bg-gray-100 text-gray-500">
                                                                <History className="w-3.5 h-3.5" />
                                                            </button>
                                                            <button onClick={() => openEditModel(m)} disabled={editingModelLoading}
                                                                title="Edit" aria-label={`Edit ${m.name || m.id}`}
                                                                className="p-1.5 rounded hover:bg-gray-100 text-gray-500 disabled:opacity-50">
                                                                <Pencil className="w-3.5 h-3.5" />
                                                            </button>
                                                            <button onClick={() => refreshModel(m.id)} disabled={modelActionId === m.id}
                                                                title="Refresh now" aria-label={`Refresh ${m.name || m.id}`}
                                                                className="p-1.5 rounded hover:bg-gray-100 text-gray-500 disabled:opacity-50">
                                                                <RefreshCw className={`w-3.5 h-3.5 ${modelActionId === m.id ? 'animate-spin' : ''}`} />
                                                            </button>
                                                            <button onClick={() => deleteModel(m.id)} disabled={modelActionId === m.id}
                                                                title="Delete" aria-label={`Delete ${m.name || m.id}`}
                                                                className="p-1.5 rounded hover:bg-red-50 text-gray-500 hover:text-red-600 disabled:opacity-50">
                                                                <Trash2 className="w-3.5 h-3.5" />
                                                            </button>
                                                        </div>
                                                    </div>
                                                    <div className="text-sm text-gray-800 leading-relaxed">
                                                        {/* Structural "still generating" signal (never refreshed and not
                                                            explicitly fresh) with the placeholder-text check as fallback. */}
                                                        {!m.content || (!m.last_refreshed_at && m.is_stale !== false) || /^generating content/i.test(m.content)
                                                            ? <span className="italic text-gray-500">Still generating…</span>
                                                            : (
                                                                <div className={MD_CLASSES}>
                                                                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{m.content}</ReactMarkdown>
                                                                </div>
                                                            )}
                                                    </div>
                                                    {Array.isArray(m.tags) && m.tags.length > 0 && (
                                                        <div className="flex flex-wrap gap-1 mt-1.5">
                                                            {m.tags.map((t) => (
                                                                <span key={t} className="text-[10px] bg-gray-100 text-gray-600 px-1.5 py-0.5 rounded-full">{t}</span>
                                                            ))}
                                                        </div>
                                                    )}
                                                    {m.last_refreshed_at && (
                                                        <div className="text-[11px] text-gray-400 mt-1.5">
                                                            Last refreshed: {new Date(m.last_refreshed_at).toLocaleString()}
                                                        </div>
                                                    )}
                                                    {expandedHistoryId === m.id && (
                                                        <div className="mt-2 border-t pt-2">
                                                            {historyLoadingId === m.id ? (
                                                                <div className="text-xs text-gray-500 italic">Loading history…</div>
                                                            ) : historyError[m.id] ? (
                                                                <div className="text-xs text-red-600">{historyError[m.id]}</div>
                                                            ) : (historyByModel[m.id] || []).length === 0 ? (
                                                                <div className="text-xs text-gray-500 italic">No history yet.</div>
                                                            ) : (
                                                                <div className="space-y-2 max-h-64 overflow-y-auto">
                                                                    {(historyByModel[m.id] || []).map((h, i) => {
                                                                        const key = `${m.id}:${i}`;
                                                                        const isLong = h.content && h.content.length > 300;
                                                                        const expanded = !!expandedHistoryEntries[key];
                                                                        return (
                                                                            <div key={key} className="bg-gray-50 border rounded-md p-2">
                                                                                <div className="text-[11px] text-gray-400 mb-1">
                                                                                    {new Date(h.refreshed_at).toLocaleString()}
                                                                                </div>
                                                                                <div className="text-xs text-gray-700 whitespace-pre-wrap">
                                                                                    {isLong && !expanded ? truncate(h.content, 300) : h.content}
                                                                                </div>
                                                                                {isLong && (
                                                                                    <button
                                                                                        onClick={() => setExpandedHistoryEntries((prev) => ({ ...prev, [key]: !expanded }))}
                                                                                        className="flex items-center gap-1 text-[11px] text-indigo-600 hover:text-indigo-800 mt-1">
                                                                                        {expanded ? <><ChevronUp className="w-3 h-3" /> Show less</> : <><ChevronDown className="w-3 h-3" /> Show more</>}
                                                                                    </button>
                                                                                )}
                                                                            </div>
                                                                        );
                                                                    })}
                                                                </div>
                                                            )}
                                                        </div>
                                                    )}
                                                </div>
                                            ))}
                                        </div>
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

            {showModelForm && selectedBankId && (
                <MentalModelForm
                    bankId={selectedBankId}
                    model={editingModel}
                    agentConfigs={agentConfigs}
                    projects={projects}
                    bankTags={bankTags}
                    onClose={() => { setShowModelForm(false); setEditingModel(null); }}
                    onSaved={onModelFormSaved}
                />
            )}
        </div>
    );
};
