import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { useStore } from '../store';
import { LogEntry, LegacyLogBlock } from '../components/LogEntry';
import { Eye, Code, RefreshCw } from 'lucide-react';

interface LogEntryData {
    id: string;
    timestamp: string;
    type: 'info' | 'error' | 'warning' | 'request' | 'response' | 'tool_call' | 'tool_result';
    content: string;
    fullContent?: string;
    metadata?: Record<string, any>;
}

function parseLogContent(logContent: string | undefined): { entries: LogEntryData[]; isLegacy: boolean } {
    if (!logContent) return { entries: [], isLegacy: false };
    
    try {
        const parsed = JSON.parse(logContent);
        if (Array.isArray(parsed)) {
            return { entries: parsed, isLegacy: false };
        }
        if (typeof parsed === 'object' && parsed.id) {
            return { entries: [parsed], isLegacy: false };
        }
    } catch {
        return { entries: [], isLegacy: true };
    }
    
    return { entries: [], isLegacy: false };
}

export const RunLogs: React.FC = () => {
    const { selectedCompanyId } = useStore();
    const { shortName } = useParams<{shortName: string}>();
    const [runs, setRuns] = useState<any[]>([]);
    const [showRaw, setShowRaw] = useState(false);
    const [rerunning, setRerunning] = useState<Record<number, boolean>>({});

    const handleRerun = async (runId: number) => {
        setRerunning(prev => ({ ...prev, [runId]: true }));
        try {
            await axios.post(`/api/runs/${runId}/rerun`);
        } catch (e) {
            console.error(e);
        } finally {
            setRerunning(prev => ({ ...prev, [runId]: false }));
        }
    };

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
            if (msg.type === 'run_log_entry') {
                setRuns((prev) => {
                    const runId = msg.payload.run_id;
                    const entry = msg.payload.entry;
                    return prev.map(r => {
                        if (r.id !== runId) return r;
                        const { entries } = parseLogContent(r.log_content);
                        return { ...r, log_content: JSON.stringify([...entries, entry]) };
                    });
                });
            } else if (msg.type === 'run_log') {
                setRuns((prev) => {
                    const runId = msg.payload.run_id;
                    const line = msg.payload.line;
                    return prev.map(r => {
                        if (r.id !== runId) return r;
                        const { entries, isLegacy } = parseLogContent(r.log_content);
                        if (isLegacy) {
                            return { ...r, log_content: (r.log_content || '') + line + '\n' };
                        } else {
                            const newEntry: LogEntryData = {
                                id: `ws-${Date.now()}`,
                                timestamp: new Date().toISOString(),
                                type: line.startsWith('ERROR') || line.startsWith('❌') ? 'error' : 
                                      line.startsWith('WARNING') || line.startsWith('️') ? 'warning' : 'info',
                                content: line,
                            };
                            return { ...r, log_content: JSON.stringify([...entries, newEntry]) };
                        }
                    });
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

    return (
        <div className="h-full flex flex-col">
            <div className="flex items-center justify-between mb-6">
                <h1 className="text-2xl font-bold">Run Logs</h1>
                <button
                    onClick={() => setShowRaw(!showRaw)}
                    className="flex items-center gap-2 px-3 py-2 text-sm border rounded-lg hover:bg-gray-50"
                >
                    {showRaw ? <Eye size={16} /> : <Code size={16} />}
                    {showRaw ? 'Show Formatted' : 'Show Raw'}
                </button>
            </div>
            <div className="flex-1 bg-white p-6 rounded-lg shadow border overflow-y-auto">
                {runs.length === 0 ? (
                    <div className="text-gray-500 italic flex items-center justify-center h-full font-mono text-sm">No agent runs recorded yet...</div>
                ) : (
                    <div className="space-y-4">
                        {runs.map((r: any) => {
                            const { entries, isLegacy } = parseLogContent(r.log_content);
                            return (
                                <details key={r.id} className="bg-gray-50 border rounded p-4 text-sm">
                                    <summary className="font-semibold cursor-pointer text-indigo-700 flex justify-between items-center">
                                        <span>Run #{r.id} for Task #{r.task_id} by {r.agent?.name} ({r.status}) - {(() => { const d = new Date(r.started_at); return d.getFullYear() > 1 ? d.toLocaleString() : (r.ended_at ? new Date(r.ended_at).toLocaleString() : '...'); })()}</span>
                                        <div className="flex items-center gap-2 ml-4">
                                            {r.status === 'failed' && (
                                                <button
                                                    onClick={(e) => { e.preventDefault(); handleRerun(r.id); }}
                                                    disabled={rerunning[r.id]}
                                                    className="text-xs bg-green-100 text-green-700 px-2 py-1 rounded hover:bg-green-200 flex items-center gap-1 disabled:opacity-50"
                                                >
                                                    <RefreshCw size={12} className={rerunning[r.id] ? 'animate-spin' : ''} />
                                                    Re-run
                                                </button>
                                            )}
                                            <Link to={`/companies/${shortName}/run-logs/${r.id}`} className="text-xs bg-indigo-100 text-indigo-700 px-2 py-1 rounded hover:bg-indigo-200" onClick={(e) => e.stopPropagation()}>
                                                View Details
                                            </Link>
                                        </div>
                                    </summary>
                                    <div className="mt-4 max-h-[500px] overflow-y-auto pr-2">
                                        {isLegacy ? (
                                            <LegacyLogBlock content={r.log_content || 'Waiting for logs...'} showRaw={showRaw} />
                                        ) : entries.length > 0 ? (
                                            entries.map((entry) => (
                                                <LogEntry key={entry.id} entry={entry} showRaw={showRaw} />
                                            ))
                                        ) : (
                                            <pre className="text-xs bg-gray-900 text-green-400 p-3 rounded overflow-x-auto whitespace-pre-wrap">
                                                {r.log_content || 'Waiting for logs...'}
                                            </pre>
                                        )}
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
