import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { CallLog } from './CallLog';
import { PHASES, errorMessage, formatDateTime, formatDuration, formatTokens, phaseLabel, rowQuery } from '../lib/workflow';
import type { LLMCall, UsageGroup, UsageReport, UsageTotals } from '../lib/workflow';

// A dimension usage is broken down by. `param` is the filter that narrows the
// ledger to one of its rows, which is how a row expands into its calls.
interface Dimension {
    key: string;
    title: string;
    param: string;
    label: (group: UsageGroup) => string;
}

const DIMENSIONS: Record<string, Dimension> = {
    phase: { key: 'phase', title: 'By workflow step', param: 'phase', label: g => phaseLabel(g.key) },
    task: { key: 'task', title: 'By task', param: 'task_id', label: g => g.label || `Task #${g.key}` },
    root_task: { key: 'root_task', title: 'By task', param: 'task_id', label: g => g.label || (g.key ? `Task #${g.key}` : 'Outside a task') },
    agent: { key: 'agent', title: 'By agent', param: 'agent_id', label: g => g.label || 'No agent' },
    model: { key: 'model', title: 'By model', param: 'model', label: g => g.key || 'unknown' },
};

const total = (t: UsageTotals): number => t.prompt_tokens + t.completion_tokens;

const sortPhases = (groups: UsageGroup[]): UsageGroup[] =>
    [...groups].sort((a, b) => {
        const ia = PHASES.indexOf(a.key), ib = PHASES.indexOf(b.key);
        return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib);
    });

const Totals: React.FC<{ totals: UsageTotals }> = ({ totals }) => (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-5" data-testid="usage-totals">
        {[
            { label: 'Tokens', value: formatTokens(total(totals)), testId: 'usage-total-tokens' },
            { label: 'Prompt', value: formatTokens(totals.prompt_tokens) },
            { label: 'Completion', value: formatTokens(totals.completion_tokens) },
            { label: 'Model calls', value: totals.failed_calls ? `${totals.calls} (${totals.failed_calls} failed)` : String(totals.calls) },
            { label: 'Model time', value: formatDuration(totals.duration_ms) },
        ].map(item => (
            <div key={item.label} className="rounded-lg border bg-white p-3">
                <p className="text-[11px] uppercase tracking-wide text-gray-400">{item.label}</p>
                <p data-testid={item.testId} className="text-lg font-semibold text-gray-900">{item.value}</p>
            </div>
        ))}
    </div>
);

interface CallPage {
    calls: LLMCall[];
    next: number | null;
}

async function fetchCalls(query: string, before: number | null): Promise<CallPage> {
    const res = await axios.get(`/api/usage/calls?${query}&limit=50${before ? `&before=${before}` : ''}`);
    return { calls: res.data.calls || [], next: res.data.next_before || null };
}

// CallList is the calls behind one usage row, newest first, a page at a time.
const CallList: React.FC<{ query: string; onOpen: (id: number) => void }> = ({ query, onOpen }) => {
    const [calls, setCalls] = useState<LLMCall[]>([]);
    const [next, setNext] = useState<number | null>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    useEffect(() => {
        let current = true;
        fetchCalls(query, null)
            .then(page => { if (current) { setCalls(page.calls); setNext(page.next); setError(null); } })
            .catch(e => { if (current) setError(errorMessage(e, 'Could not load the calls')); })
            .finally(() => { if (current) setLoading(false); });
        return () => { current = false; };
    }, [query]);

    const more = () => {
        setLoading(true);
        fetchCalls(query, next)
            .then(page => { setCalls(previous => [...previous, ...page.calls]); setNext(page.next); })
            .catch(e => setError(errorMessage(e, 'Could not load the calls')))
            .finally(() => setLoading(false));
    };

    if (error) return <p className="px-3 py-2 text-xs text-red-600">{error}</p>;
    return (
        <div className="bg-gray-50" data-testid="usage-calls">
            {calls.map(call => (
                <button
                    key={call.id}
                    onClick={() => onOpen(call.id)}
                    data-testid="usage-call"
                    className="grid w-full grid-cols-12 gap-2 border-t border-gray-100 px-3 py-1.5 text-left text-xs hover:bg-indigo-50"
                >
                    <span className="col-span-3 truncate text-gray-500">{formatDateTime(call.created_at)}</span>
                    <span className="col-span-2 truncate">{call.agent_name || '—'}</span>
                    <span className="col-span-2 truncate text-gray-500">{call.tier} · {phaseLabel(call.phase)}</span>
                    <span className="col-span-3 truncate font-mono text-gray-600">{call.model || call.requested_model}</span>
                    <span className={`col-span-2 text-right ${call.status === 'ok' ? 'text-gray-700' : 'font-medium text-red-600'}`}>
                        {call.status === 'ok' ? `${formatTokens(call.prompt_tokens)} / ${formatTokens(call.completion_tokens)}` : call.status}
                    </span>
                </button>
            ))}
            {loading && <p className="px-3 py-2 text-xs text-gray-400">Loading…</p>}
            {!loading && calls.length === 0 && <p className="px-3 py-2 text-xs text-gray-400">No calls.</p>}
            {!loading && next && (
                <button onClick={more} className="w-full border-t border-gray-100 px-3 py-1.5 text-xs text-indigo-600 hover:bg-indigo-50">Show more</button>
            )}
        </div>
    );
};

interface UsageTableProps {
    dimension: Dimension;
    groups: UsageGroup[];
    // tiers splits a row into smart and cheap spend ("phase/tier" keys).
    tiers?: UsageGroup[];
    baseQuery: string;
    onOpen: (id: number) => void;
}

const UsageTable: React.FC<UsageTableProps> = ({ dimension, groups, tiers, baseQuery, onOpen }) => {
    const [expanded, setExpanded] = useState<string | null>(null);
    const rows = dimension.key === 'phase' ? sortPhases(groups) : groups;
    const max = Math.max(1, ...rows.map(total));
    const tierOf = (key: string, tier: string): number => {
        const group = tiers?.find(t => t.key === `${key}/${tier}`);
        return group ? total(group) : 0;
    };

    return (
        <div className="overflow-hidden rounded-lg border bg-white" data-testid={`usage-by-${dimension.key}`}>
            <div className="border-b bg-gray-50 px-3 py-2 text-sm font-semibold text-gray-700">{dimension.title}</div>
            {rows.length === 0 && <p className="px-3 py-3 text-xs text-gray-400">Nothing yet.</p>}
            {rows.map(group => {
                const open = expanded === group.key;
                // A row whose key is empty (no task, no agent, no step) has no
                // filter to narrow by, so it cannot be opened.
                const canExpand = group.key !== '';
                const filter = rowQuery(baseQuery, dimension.key, dimension.param, group.key);
                return (
                    <div key={group.key} className="border-t border-gray-100 first:border-t-0" data-testid="usage-row">
                        <button
                            onClick={() => canExpand && setExpanded(open ? null : group.key)}
                            className={`flex w-full items-center gap-2 px-3 py-2 text-left text-sm ${canExpand ? 'hover:bg-gray-50' : 'cursor-default'}`}
                        >
                            {canExpand ? (open ? <ChevronDown size={14} className="text-gray-400" /> : <ChevronRight size={14} className="text-gray-400" />) : <span className="w-[14px]" />}
                            <span className="min-w-0 flex-1 truncate">{dimension.label(group)}</span>
                            {tiers && (
                                <span className="hidden text-xs text-gray-400 md:inline" title="Smart model tokens / cheap model tokens">
                                    smart {formatTokens(tierOf(group.key, 'smart'))} · cheap {formatTokens(tierOf(group.key, 'cheap'))}
                                </span>
                            )}
                            <span className="w-16 text-right text-xs text-gray-500">{group.calls} calls</span>
                            <span className="hidden w-24 md:block">
                                <span className="block h-1.5 rounded bg-indigo-100">
                                    <span className="block h-1.5 rounded bg-indigo-500" style={{ width: `${Math.round((total(group) / max) * 100)}%` }} />
                                </span>
                            </span>
                            <span className="w-14 text-right font-medium tabular-nums" data-testid="usage-row-tokens">{formatTokens(total(group))}</span>
                        </button>
                        {open && <CallList query={filter} onOpen={onOpen} />}
                    </div>
                );
            })}
        </div>
    );
};

interface UsagePanelProps {
    // Where the report comes from and which breakdowns to show.
    reportUrl: string;
    // The query that narrows /api/usage/calls to the same set of calls.
    callsQuery: string;
    dimensions: string[];
    // Bumping it reloads the report (for example when a task makes progress).
    refreshSignal?: number;
}

// UsagePanel shows what a set of model calls cost: totals, a breakdown for
// each dimension, every row expandable to its calls and every call openable
// as a log. All of it is read from the same ledger, so the numbers agree.
export const UsagePanel: React.FC<UsagePanelProps> = ({ reportUrl, callsQuery, dimensions, refreshSignal }) => {
    const [report, setReport] = useState<UsageReport | null>(null);
    const [error, setError] = useState<string | null>(null);
    const [openCall, setOpenCall] = useState<number | null>(null);

    useEffect(() => {
        let current = true;
        axios.get(reportUrl)
            .then(res => { if (current) { setReport(res.data); setError(null); } })
            .catch(e => { if (current) setError(errorMessage(e, 'Could not load usage')); });
        return () => { current = false; };
    }, [reportUrl, refreshSignal]);

    if (error) return <div className="rounded border border-red-200 bg-red-50 p-3 text-sm text-red-800">{error}</div>;
    if (!report) return <p className="text-sm text-gray-500">Loading usage…</p>;

    return (
        <div className="space-y-4" data-testid="usage-panel">
            <Totals totals={report.totals} />
            <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
                {dimensions.map(key => (
                    <UsageTable
                        key={key}
                        dimension={DIMENSIONS[key]}
                        groups={report.groups[key] || []}
                        tiers={key === 'phase' ? report.groups.phase_tier : undefined}
                        baseQuery={callsQuery}
                        onOpen={setOpenCall}
                    />
                ))}
            </div>
            {openCall !== null && <CallLog key={openCall} callId={openCall} onClose={() => setOpenCall(null)} />}
        </div>
    );
};
