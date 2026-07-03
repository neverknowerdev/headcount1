import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { useStore } from '../store';
import { useWebSocket, wsUrl } from '../useWebSocket';

export const RunLogs: React.FC = () => {
    const { selectedCompanyId } = useStore();
    const { shortName } = useParams<{shortName: string}>();
    const [runs, setRuns] = useState<any[]>([]);

    const fetchRuns = useCallback(async () => {
        if (!selectedCompanyId) return;
        try {
            const res = await axios.get(`/api/runs?company_id=${selectedCompanyId}`);
            setRuns(res.data || []);
        } catch (e) {
            console.error(e);
        }
    }, [selectedCompanyId]);

    useEffect(() => { fetchRuns(); }, [fetchRuns]);

    useWebSocket(wsUrl(), (msg) => {
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
    }, {
        enabled: !!selectedCompanyId,
        // Re-fetch on every (re)connect so events missed while offline are recovered.
        onConnect: fetchRuns,
    });

    const getLogPreview = (r: any): string => {
        if (r.log_entries && r.log_entries.length > 0) {
            const lastInfo = [...r.log_entries].reverse().find((e: any) => e.type === 'info');
            return lastInfo?.content?.substring(0, 100) || 'Processing...';
        }
        if (r.log_content) {
            const lines = r.log_content.split('\n').filter((l: string) => l.trim());
            return lines[lines.length - 1]?.substring(0, 100) || 'Waiting for logs...';
        }
        return 'Waiting for logs...';
    };

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
                    <div className="space-y-4">
                        {runs.map((r: any) => {
                            const ts = r.token_stats || {};
                            const total = ts.total_tokens || 0;
                            return (
                            <details key={r.id} className="bg-gray-50 border rounded p-4 text-sm">
                                <summary className="font-semibold cursor-pointer text-indigo-700 flex justify-between items-center gap-2 flex-wrap">
                                    <span>Run #{r.id} for Task #{r.task_id} by {r.agent?.name} ({r.status}) - {(() => { const d = new Date(r.started_at); return d.getFullYear() > 1 ? d.toLocaleString() : (r.ended_at ? new Date(r.ended_at).toLocaleString() : '...'); })()}</span>
                                    <div className="flex items-center gap-2">
                                        {total > 0 && (
                                            <div className="flex items-center gap-1 text-xs font-mono">
                                                <span className="bg-gray-100 px-1.5 py-0.5 rounded text-gray-700">{formatTokens(total)} tok</span>
                                                {ts.reasoning_tokens > 0 && (
                                                    <span className="bg-purple-50 text-purple-700 px-1.5 py-0.5 rounded" title="reasoning tokens">⊕{formatTokens(ts.reasoning_tokens)}</span>
                                                )}
                                            </div>
                                        )}
                                        <Link to={`/companies/${shortName}/run-logs/${r.id}`} className="text-xs bg-indigo-100 text-indigo-700 px-2 py-1 rounded hover:bg-indigo-200">
                                            View Details
                                        </Link>
                                    </div>
                                </summary>
                                <div className="mt-4 text-xs bg-gray-900 text-green-400 p-3 rounded overflow-x-auto whitespace-pre-wrap max-h-96">
                                    {r.status === 'running' && (
                                        <div className="flex items-center gap-2 text-yellow-400 mb-2">
                                            <div className="w-2 h-2 bg-yellow-400 rounded-full animate-pulse" />
                                            Running...
                                        </div>
                                    )}
                                    <pre className="whitespace-pre-wrap">{getLogPreview(r)}</pre>
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
