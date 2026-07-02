import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { useStore } from '../store';
import { RunLogViewer } from '../components/RunLogViewer';

export const RunLogs: React.FC = () => {
    const { selectedCompanyId } = useStore();
    const { shortName } = useParams<{shortName: string}>();
    const [runs, setRuns] = useState<any[]>([]);

    useEffect(() => {
        if (!selectedCompanyId) return;

        const fetchRuns = async () => {
            try {
                const res = await axios.get(`/api/runs?company_id=${selectedCompanyId}`);
                setRuns(res.data || []);
            } catch (e) {
                console.error(e);
            }
        };

        fetchRuns();

        const ws = new WebSocket(`ws://${window.location.host}/api/ws`);
        ws.onmessage = (event) => {
            const msg = JSON.parse(event.data);
            if (msg.type === 'run_log') {
                setRuns((prev) => {
                    const runId = msg.payload.run_id;
                    if (msg.payload.entry) {
                        // Structured format
                        return prev.map(r =>
                            r.id === runId
                                ? { ...r, log_entries: [...(r.log_entries || []), msg.payload.entry] }
                                : r
                        );
                    } else if (msg.payload.line) {
                        // Legacy format fallback
                        return prev.map(r =>
                            r.id === runId
                                ? { ...r, log_content: (r.log_content || '') + msg.payload.line + '\n' }
                                : r
                        );
                    }
                    return prev;
                });
            } else if (msg.type === 'run_started') {
                fetchRuns();
            } else if (msg.type === 'run_ended') {
                setRuns((prev) => {
                    const runId = msg.payload.run_id;
                    const status = msg.payload.status;
                    return prev.map(r =>
                        r.id === runId
                            ? { ...r, status }
                            : r
                    );
                });
            }
        };
        return () => ws.close();
    }, [selectedCompanyId]);

    const formatTokens = (n: number): string => {
        if (!n || n < 1000) return String(n || 0);
        if (n < 10000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'K';
        if (n < 1000000) return Math.round(n / 1000) + 'K';
        return (n / 1000000).toFixed(1).replace(/\.0$/, '') + 'M';
    };

    return (
        <div className="h-full flex flex-col">
            <h1 className="text-2xl font-bold mb-6">Run Logs</h1>
            <div className="flex-1 bg-white p-6 rounded-lg shadow border overflow-y-auto">
                {runs.length === 0 ? (
                    <div className="text-gray-500 italic flex items-center justify-center h-full font-mono text-sm">No agent runs recorded yet...</div>
                ) : (
                    <div className="space-y-3">
                        {(() => {
                            // Only main (root) sessions form the top level of the list.
                            // Delegated sessions are nested one level down and rendered
                            // by RunLogViewer's own session blocks, which stay collapsed
                            // until the user expands ("maximizes") them individually.
                            const childCountByParent = new Map<number, number>();
                            runs.forEach((r: any) => {
                                if (r.parent_run_id) {
                                    childCountByParent.set(r.parent_run_id, (childCountByParent.get(r.parent_run_id) || 0) + 1);
                                }
                            });
                            const rootRuns = runs.filter((r: any) => !r.parent_run_id);
                            return rootRuns.map((r: any) => {
                            const ts = r.token_stats || {};
                            const total = ts.total_tokens || 0;
                            const childCount = childCountByParent.get(r.id) || 0;
                            const messages = (r.log_entries || []).map((e: any, i: number) => ({ id: i, entry: e }));
                            return (
                            <details key={r.id} className="bg-gray-50 border rounded-lg overflow-hidden text-sm" data-testid="root-run-card">
                                <summary className="px-4 py-3 font-semibold cursor-pointer text-indigo-700 flex justify-between items-center gap-2 flex-wrap hover:bg-gray-100">
                                    <span>
                                        Run #{r.id} for Task #{r.task_id} by {r.agent?.name}
                                        {r.agent_config_name ? ` · ${r.agent_config_name}` : ''} ({r.status}) - {(() => { const d = new Date(r.started_at); return d.getFullYear() > 1 ? d.toLocaleString() : (r.ended_at ? new Date(r.ended_at).toLocaleString() : '...'); })()}
                                        {childCount > 0 && (
                                            <span className="ml-2 text-xs bg-violet-100 text-violet-700 px-1.5 py-0.5 rounded-full">{childCount} session{childCount > 1 ? 's' : ''}</span>
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
                                <div className="border-t h-[28rem]">
                                    <RunLogViewer messages={messages} status={r.status} tokenStats={r.token_stats} />
                                </div>
                            </details>
                            );
                        });
                        })()}
                    </div>
                )}
            </div>
        </div>
    );
};
