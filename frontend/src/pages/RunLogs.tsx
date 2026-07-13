import React, { useState, useEffect, useCallback, useMemo } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { useStore } from '../store';
import { TokenStatsBar } from '../components/RunLogViewer';
import { buildAgentStats } from '../utils/runStats';
import { useWebSocket, wsUrl } from '../useWebSocket';

const formatTokens = (n: number): string => {
    if (!n || n < 1000) return String(n || 0);
    if (n < 10000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'K';
    if (n < 1000000) return Math.round(n / 1000) + 'K';
    return (n / 1000000).toFixed(1).replace(/\.0$/, '') + 'M';
};

const formatDateTime = (value?: string | null): string => {
    if (!value) return '—';
    const d = new Date(value);
    return d.getFullYear() > 1 ? d.toLocaleString() : '—';
};

// Duration is computed client-side from timestamps rather than fetched, so
// it stays accurate for still-running runs without extra server work.
const formatDuration = (startedAt?: string | null, endedAt?: string | null): string => {
    if (!startedAt) return '—';
    const start = new Date(startedAt);
    if (start.getFullYear() <= 1) return '—';
    const end = endedAt ? new Date(endedAt) : new Date();
    const ms = end.getTime() - start.getTime();
    if (ms < 0) return '—';
    const sec = Math.floor(ms / 1000);
    if (sec < 60) return `${sec}s`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ${sec % 60}s`;
    const hr = Math.floor(min / 60);
    return `${hr}h ${min % 60}m`;
};

const InfoItem: React.FC<{ label: string; value: React.ReactNode; className?: string }> = ({ label, value, className }) => (
    <div className={className}>
        <p className="text-[11px] uppercase tracking-wide text-gray-400">{label}</p>
        <p className="text-sm text-gray-800 truncate">{value}</p>
    </div>
);

interface SystemLLMLog {
    id: number;
    source: string;
    detail: string;
    provider_name: string;
    model: string;
    prompt_tokens: number;
    completion_tokens: number;
    total_tokens: number;
    duration_ms: number;
    status: string;
    error: string;
    created_at: string;
}

const SYSTEM_LOG_PAGE_SIZE = 50;

const SOURCE_BADGE_CLASSES: Record<string, string> = {
    memory: 'bg-violet-100 text-violet-700',
    ask_artifact: 'bg-sky-100 text-sky-700',
    commit_message: 'bg-emerald-100 text-emerald-700',
};

const SystemLLMLogs: React.FC = () => {
    const [logs, setLogs] = useState<SystemLLMLog[]>([]);
    const [total, setTotal] = useState(0);
    const [source, setSource] = useState('');
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [expandedId, setExpandedId] = useState<number | null>(null);
    const [detail, setDetail] = useState<{ log: SystemLLMLog; file?: string } | null>(null);
    const [detailLoading, setDetailLoading] = useState(false);
    const [detailError, setDetailError] = useState<string | null>(null);

    const fetchLogs = useCallback(async (offset: number, src: string) => {
        setLoading(true);
        setError(null);
        try {
            const params = new URLSearchParams({ limit: String(SYSTEM_LOG_PAGE_SIZE), offset: String(offset) });
            if (src) params.set('source', src);
            const res = await axios.get(`/api/system-llm-logs?${params.toString()}`);
            const items: SystemLLMLog[] = res.data?.items || [];
            setTotal(res.data?.total || 0);
            setLogs((prev) => (offset === 0 ? items : [...prev, ...items]));
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to load system LLM logs');
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        setExpandedId(null);
        setDetail(null);
        fetchLogs(0, source);
    }, [source, fetchLogs]);

    const openDetail = useCallback(async (id: number) => {
        if (expandedId === id) {
            setExpandedId(null);
            setDetail(null);
            return;
        }
        setExpandedId(id);
        setDetail(null);
        setDetailError(null);
        setDetailLoading(true);
        try {
            const res = await axios.get(`/api/system-llm-logs/${id}`);
            setDetail({ log: res.data?.log, file: res.data?.file });
        } catch (e: any) {
            setDetailError(e?.response?.data?.error || 'Failed to load log detail');
        } finally {
            setDetailLoading(false);
        }
    }, [expandedId]);

    return (
        <div className="flex flex-col h-full">
            <div className="flex items-center gap-1 mb-4 flex-wrap">
                {([
                    ['', 'All'],
                    ['memory', 'memory'],
                    ['ask_artifact', 'ask_artifact'],
                    ['commit_message', 'commit_message'],
                ] as [string, string][]).map(([value, label]) => (
                    <button key={value} onClick={() => setSource(value)}
                        className={`px-3 py-1.5 text-sm font-medium rounded-md ${
                            source === value ? 'bg-indigo-50 text-indigo-600' : 'text-gray-600 hover:bg-gray-50 hover:text-gray-900'
                        }`}>
                        {label}
                    </button>
                ))}
            </div>
            {error && <div className="text-red-600 text-sm mb-3">{error}</div>}
            {logs.length === 0 && !loading ? (
                <div className="text-gray-500 italic flex items-center justify-center h-full font-mono text-sm">No system LLM calls recorded yet...</div>
            ) : (
                <div className="overflow-x-auto">
                    <table className="w-full text-sm" data-testid="system-llm-logs-table">
                        <thead>
                            <tr className="text-left text-xs text-gray-500 border-b">
                                <th className="px-3 py-2 font-medium">Time</th>
                                <th className="px-3 py-2 font-medium">Source</th>
                                <th className="px-3 py-2 font-medium">Detail</th>
                                <th className="px-3 py-2 font-medium">Model</th>
                                <th className="px-3 py-2 font-medium">Provider</th>
                                <th className="px-3 py-2 font-medium text-right">Tokens (p/c/t)</th>
                                <th className="px-3 py-2 font-medium text-right">Duration</th>
                                <th className="px-3 py-2 font-medium">Status</th>
                            </tr>
                        </thead>
                        <tbody>
                            {logs.map((l) => (
                                <React.Fragment key={l.id}>
                                    <tr onClick={() => openDetail(l.id)}
                                        className={`border-b cursor-pointer hover:bg-gray-50 ${expandedId === l.id ? 'bg-indigo-50/50' : ''}`}
                                        data-testid="system-llm-log-row">
                                        <td className="px-3 py-2 whitespace-nowrap text-gray-600">{new Date(l.created_at).toLocaleString()}</td>
                                        <td className="px-3 py-2">
                                            <span className={`text-xs px-2 py-0.5 rounded-full ${SOURCE_BADGE_CLASSES[l.source] || 'bg-gray-100 text-gray-700'}`}>
                                                {l.source}
                                            </span>
                                        </td>
                                        <td className="px-3 py-2 text-gray-700 max-w-[16rem] truncate" title={l.detail}>{l.detail || ''}</td>
                                        <td className="px-3 py-2 font-mono text-xs text-gray-700">{l.model}</td>
                                        <td className="px-3 py-2 text-gray-600">{l.provider_name}</td>
                                        <td className="px-3 py-2 text-right font-mono text-xs text-gray-700 whitespace-nowrap">
                                            {l.prompt_tokens} / {l.completion_tokens} / {l.total_tokens}
                                        </td>
                                        <td className="px-3 py-2 text-right font-mono text-xs text-gray-700 whitespace-nowrap">{l.duration_ms} ms</td>
                                        <td className="px-3 py-2">
                                            {l.status === 'error' ? (
                                                <span className="text-red-600 text-xs font-medium" title={l.error}>
                                                    error{l.error ? `: ${l.error.length > 60 ? l.error.slice(0, 59) + '…' : l.error}` : ''}
                                                </span>
                                            ) : (
                                                <span className="text-green-600 text-xs font-medium">ok</span>
                                            )}
                                        </td>
                                    </tr>
                                    {expandedId === l.id && (
                                        <tr className="border-b bg-gray-50">
                                            <td colSpan={8} className="px-3 py-3">
                                                {detailLoading ? (
                                                    <div className="text-gray-500 italic text-sm">Loading…</div>
                                                ) : detailError ? (
                                                    <div className="text-red-600 text-sm">{detailError}</div>
                                                ) : detail ? (
                                                    <div className="space-y-2">
                                                        {detail.log?.status === 'error' && detail.log.error && (
                                                            <div className="text-sm text-red-600 whitespace-pre-wrap bg-red-50 border border-red-200 rounded-md p-2">
                                                                {detail.log.error}
                                                            </div>
                                                        )}
                                                        {detail.file ? (
                                                            <pre className="text-xs bg-gray-900 text-gray-100 rounded-md p-3 overflow-auto max-h-96 whitespace-pre">{detail.file}</pre>
                                                        ) : (
                                                            <div className="text-gray-500 italic text-sm">Log file not available.</div>
                                                        )}
                                                    </div>
                                                ) : null}
                                            </td>
                                        </tr>
                                    )}
                                </React.Fragment>
                            ))}
                        </tbody>
                    </table>
                    {logs.length < total && (
                        <div className="flex justify-center py-3">
                            <button onClick={() => fetchLogs(logs.length, source)} disabled={loading}
                                className="px-3 py-1.5 text-sm border rounded-md bg-white hover:bg-gray-50 text-gray-700 disabled:opacity-50">
                                {loading ? 'Loading…' : `Load more (${logs.length} of ${total})`}
                            </button>
                        </div>
                    )}
                </div>
            )}
        </div>
    );
};

export const RunLogs: React.FC = () => {
    const { selectedCompanyId } = useStore();
    const { shortName } = useParams<{shortName: string}>();
    const [runs, setRuns] = useState<any[]>([]);
    const [loading, setLoading] = useState(true);
    const [view, setView] = useState<'runs' | 'system'>('runs');

    const fetchRuns = useCallback(async () => {
        if (!selectedCompanyId) return;
        try {
            // This endpoint returns overview fields only (no log transcripts),
            // so the list renders immediately even with a long run history.
            // Full content is fetched lazily on the Run Log Details page.
            const res = await axios.get(`/api/runs?company_id=${selectedCompanyId}`);
            setRuns(res.data || []);
        } catch (e) {
            console.error(e);
        } finally {
            setLoading(false);
        }
    }, [selectedCompanyId]);

    useEffect(() => { fetchRuns(); }, [fetchRuns]);

    useWebSocket(wsUrl(), (msg) => {
        if (msg.type === 'run_started') {
            fetchRuns();
        } else if (msg.type === 'run_ended') {
            setRuns((prev) => {
                const runId = msg.payload.run_id;
                const status = msg.payload.status;
                return prev.map(r => r.id === runId ? { ...r, status } : r);
            });
        } else if (msg.type === 'run_status') {
            setRuns((prev) => {
                const runId = msg.payload.run_id;
                const status = msg.payload.status;
                return prev.map(r => r.id === runId ? { ...r, current_status: status } : r);
            });
        }
    }, {
        enabled: !!selectedCompanyId,
        // Re-fetch on every (re)connect so overview data missed while offline
        // (status changes, token totals) is recovered.
        onConnect: fetchRuns,
    });

    // Grouping is derived once per runs update rather than per-row during
    // render: childrenByParent feeds the "sessions" list and count badge,
    // descendantsByRoot feeds the whole-tree per-agent token breakdown.
    const { rootRuns, childrenByParent, descendantsByRoot } = useMemo(() => {
        const childrenByParent = new Map<number, any[]>();
        const descendantsByRoot = new Map<number, any[]>();
        runs.forEach((r: any) => {
            if (r.parent_run_id) {
                if (!childrenByParent.has(r.parent_run_id)) childrenByParent.set(r.parent_run_id, []);
                childrenByParent.get(r.parent_run_id)!.push(r);
            }
            const rootId = r.root_run_id || r.id;
            if (!descendantsByRoot.has(rootId)) descendantsByRoot.set(rootId, []);
            descendantsByRoot.get(rootId)!.push(r);
        });
        const rootRuns = runs.filter((r: any) => !r.parent_run_id);
        return { rootRuns, childrenByParent, descendantsByRoot };
    }, [runs]);

    return (
        <div className="h-full flex flex-col">
            <div className="flex items-center justify-between mb-6 flex-wrap gap-3">
                <h1 className="text-2xl font-bold">Run Logs</h1>
                <div className="flex items-center gap-1">
                    {([
                        ['runs', 'Agent runs'],
                        ['system', 'System LLM calls'],
                    ] as ['runs' | 'system', string][]).map(([v, label]) => (
                        <button key={v} onClick={() => setView(v)}
                            className={`px-3 py-1.5 text-sm font-medium rounded-md ${
                                view === v ? 'bg-indigo-50 text-indigo-600' : 'text-gray-600 hover:bg-gray-50 hover:text-gray-900'
                            }`}>
                            {label}
                        </button>
                    ))}
                </div>
            </div>
            <div className="flex-1 bg-white p-6 rounded-lg shadow border overflow-y-auto">
                {view === 'system' ? (
                    <SystemLLMLogs />
                ) : runs.length === 0 ? (
                    <div className="text-gray-500 italic flex items-center justify-center h-full font-mono text-sm">
                        {loading ? 'Loading runs…' : 'No agent runs recorded yet...'}
                    </div>
                ) : (
                    <div className="space-y-3">
                        {rootRuns.map((r: any) => {
                            const ts = r.token_stats || {};
                            const total = ts.total_tokens || 0;
                            const children = childrenByParent.get(r.id) || [];
                            const descendants = (descendantsByRoot.get(r.id) || []).filter((d: any) => d.id !== r.id);
                            const agentStats = descendants.length > 0 ? buildAgentStats(r, descendants) : undefined;
                            return (
                            <details key={r.id} className="bg-gray-50 border rounded-lg overflow-hidden text-sm" data-testid="root-run-card">
                                <summary className="px-4 py-3 font-semibold cursor-pointer text-indigo-700 flex justify-between items-center gap-2 flex-wrap hover:bg-gray-100">
                                    <span>
                                        Run {r.name || `#${r.id}`} for Task {r.task?.ref_key || `#${r.task_id}`} by {r.agent?.name}
                                        {!r.name && r.agent_config_name ? ` · ${r.agent_config_name}` : ''} ({r.status}) - {(() => { const d = new Date(r.started_at); return d.getFullYear() > 1 ? d.toLocaleString() : (r.ended_at ? new Date(r.ended_at).toLocaleString() : '...'); })()}
                                        {children.length > 0 && (
                                            <span className="ml-2 text-xs bg-violet-100 text-violet-700 px-1.5 py-0.5 rounded-full">{children.length} session{children.length > 1 ? 's' : ''}</span>
                                        )}
                                    </span>
                                    <div className="flex items-center gap-2">
                                        {total > 0 && (
                                            <div className="flex items-center gap-1 text-xs font-mono">
                                                <span className="bg-gray-100 px-1.5 py-0.5 rounded text-gray-700">{formatTokens(total)} tok</span>
                                                {ts.reasoning_tokens > 0 && (
                                                    <span className="bg-purple-50 text-purple-700 px-1.5 py-0.5 rounded" title="reasoning tokens">⊕{formatTokens(ts.reasoning_tokens)}</span>
                                                )}
                                            </div>
                                        )}
                                        <Link to={`/companies/${shortName}/run-logs/${r.id}`} className="text-xs bg-indigo-100 text-indigo-700 px-2 py-1 rounded hover:bg-indigo-200" onClick={e => e.stopPropagation()}>
                                            View Details
                                        </Link>
                                    </div>
                                </summary>
                                <div className="border-t p-4 space-y-4">
                                    <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
                                        <InfoItem label="Status" value={<span className="capitalize">{r.status}</span>} />
                                        <InfoItem label="Agent" value={`${r.agent?.name || '—'}${r.agent_config_name ? ` (${r.agent_config_name})` : ''}`} />
                                        <InfoItem label="Started" value={formatDateTime(r.started_at)} />
                                        <InfoItem label="Duration" value={formatDuration(r.started_at, r.ended_at)} />
                                        {r.current_status && (
                                            <InfoItem className="col-span-2 sm:col-span-4" label="Current Activity" value={<span className="text-violet-700">{r.current_status}</span>} />
                                        )}
                                    </div>

                                    {r.result_description && (
                                        <div>
                                            <p className="text-[11px] uppercase tracking-wide text-gray-400 mb-1">Result</p>
                                            <p className="text-sm text-gray-800 whitespace-pre-wrap">{r.result_description}</p>
                                        </div>
                                    )}

                                    {total > 0 && (
                                        <div>
                                            <p className="text-[11px] uppercase tracking-wide text-gray-400 mb-1">Token Usage</p>
                                            <TokenStatsBar stats={r.token_stats} messages={[]} agentStats={agentStats} />
                                        </div>
                                    )}

                                    {children.length > 0 && (
                                        <div>
                                            <p className="text-[11px] uppercase tracking-wide text-gray-400 mb-1">Sessions ({children.length})</p>
                                            <div className="flex flex-col gap-1">
                                                {children.map((c: any) => (
                                                    <Link
                                                        key={c.id}
                                                        to={`/companies/${shortName}/run-logs/${c.id}`}
                                                        data-testid="child-session-link"
                                                        className="text-xs bg-white border rounded px-2 py-1 hover:bg-gray-100 flex items-center justify-between gap-2"
                                                    >
                                                        <span className="truncate">{c.name || `#${c.id}`} {c.agent_config_name ? `· ${c.agent_config_name}` : c.agent?.name ? `· ${c.agent.name}` : ''}</span>
                                                        <span className="text-gray-500 capitalize shrink-0">{c.status}</span>
                                                    </Link>
                                                ))}
                                            </div>
                                        </div>
                                    )}

                                    <Link
                                        to={`/companies/${shortName}/run-logs/${r.id}`}
                                        className="inline-block text-xs font-medium bg-indigo-600 text-white px-3 py-1.5 rounded hover:bg-indigo-700"
                                    >
                                        View Full Log →
                                    </Link>
                                </div>
                            </details>
                            );
                        })}
                    </div>
                )}
            </div>
        </div>
    );
};
