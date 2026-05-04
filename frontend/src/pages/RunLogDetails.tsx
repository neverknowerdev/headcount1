import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { ArrowLeft, Eye, Code, RefreshCw } from 'lucide-react';
import { LogEntry, LegacyLogBlock } from '../components/LogEntry';

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

export const RunLogDetails: React.FC = () => {
    const { shortName, id } = useParams<{shortName: string, id: string}>();
    const [run, setRun] = useState<any>(null);
    const [showRaw, setShowRaw] = useState(false);
    const [rerunning, setRerunning] = useState(false);

    const handleRerun = async () => {
        if (!id) return;
        setRerunning(true);
        try {
            await axios.post(`/api/runs/${id}/rerun`);
        } catch (e) {
            console.error(e);
        } finally {
            setRerunning(false);
        }
    };

    useEffect(() => {
        const fetchRun = async () => {
            try {
                const res = await axios.get(`/api/runs/${id}`);
                setRun(res.data);
            } catch (e) {
                console.error(e);
            }
        };

        fetchRun();

        const ws = new WebSocket(`ws://${window.location.host}/api/ws`);
        ws.onmessage = (event) => {
            const msg = JSON.parse(event.data);
            if (msg.type === 'run_log_entry' && msg.payload.run_id === parseInt(id || '0')) {
                setRun((prev: any) => {
                    if (!prev) return prev;
                    const { entries } = parseLogContent(prev.log_content);
                    return { ...prev, log_content: JSON.stringify([...entries, msg.payload.entry]) };
                });
            } else if (msg.type === 'run_log' && msg.payload.run_id === parseInt(id || '0')) {
                setRun((prev: any) => {
                    if (!prev) return prev;
                    const { entries, isLegacy } = parseLogContent(prev.log_content);
                    const line = msg.payload.line;
                    if (isLegacy) {
                        return { ...prev, log_content: (prev.log_content || '') + line + '\n' };
                    } else {
                        const newEntry: LogEntryData = {
                            id: `ws-${Date.now()}`,
                            timestamp: new Date().toISOString(),
                            type: line.startsWith('ERROR') || line.startsWith('❌') ? 'error' : 
                                  line.startsWith('WARNING') || line.startsWith('️') ? 'warning' : 'info',
                            content: line,
                        };
                        return { ...prev, log_content: JSON.stringify([...entries, newEntry]) };
                    }
                });
            } else if (msg.type === 'run_ended' && msg.payload.run_id === parseInt(id || '0')) {
                setRun((prev: any) => prev ? { ...prev, status: msg.payload.status } : prev);
            }
        };
        return () => ws.close();
    }, [id]);

    if (!run) return <div>Loading...</div>;

    const { entries, isLegacy } = parseLogContent(run.log_content);

    return (
        <div className="h-full flex flex-col">
            <div className="mb-6 flex items-center justify-between">
                <div className="flex items-center space-x-4">
                    <Link to={`/companies/${shortName}/runs`} className="text-gray-500 hover:text-gray-900">
                        <ArrowLeft size={20} />
                    </Link>
                    <h1 className="text-2xl font-bold">Run #{run.id} Details</h1>
                </div>
                <div className="flex items-center gap-2">
                    {run.status === 'failed' && (
                        <button
                            onClick={handleRerun}
                            disabled={rerunning}
                            className="flex items-center gap-2 px-3 py-2 text-sm border rounded-lg hover:bg-green-50 text-green-700 border-green-200 disabled:opacity-50"
                        >
                            <RefreshCw size={16} className={rerunning ? 'animate-spin' : ''} />
                            Re-run
                        </button>
                    )}
                    <button
                        onClick={() => setShowRaw(!showRaw)}
                        className="flex items-center gap-2 px-3 py-2 text-sm border rounded-lg hover:bg-gray-50"
                    >
                        {showRaw ? <Eye size={16} /> : <Code size={16} />}
                        {showRaw ? 'Show Formatted' : 'Show Raw'}
                    </button>
                </div>
            </div>

            <div className="grid grid-cols-3 gap-6 flex-1 min-h-0">
                <div className="col-span-1 bg-white p-6 rounded-lg shadow border space-y-4">
                    <h3 className="font-bold text-lg border-b pb-2">Context</h3>
                    <div>
                        <p className="text-sm text-gray-500">Status</p>
                        <p className="font-medium capitalize">{run.status}</p>
                    </div>
                    <div>
                        <p className="text-sm text-gray-500">Mode</p>
                        <p className="font-medium capitalize">{run.mode || 'implement'}</p>
                    </div>
                    <div>
                        <p className="text-sm text-gray-500">Agent</p>
                        <p className="font-medium">{run.agent?.name}</p>
                    </div>
                    <div>
                        <p className="text-sm text-gray-500">Task</p>
                        <Link to={`/companies/${shortName}/tasks`} className="font-medium text-indigo-600 hover:underline">{run.task?.title} (#{run.task_id})</Link>
                    </div>
                    <div>
                        <p className="text-sm text-gray-500">Started At</p>
                        <p className="font-medium">{(() => { const d = new Date(run.started_at); return d.getFullYear() > 1 ? d.toLocaleString() : (run.ended_at ? new Date(run.ended_at).toLocaleString() : '...'); })()}</p>
                    </div>
                    <div>
                        <p className="text-sm text-gray-500">Session ID</p>
                        <p className="font-mono text-xs break-all">{run.session_id || 'N/A'}</p>
                    </div>
                    <div>
                        <p className="text-sm text-gray-500">Log Entries</p>
                        <p className="font-medium">{isLegacy ? 'Legacy format' : `${entries.length} entries`}</p>
                    </div>
                </div>

                <div className="col-span-2 bg-white rounded-lg shadow flex flex-col min-h-0 border">
                    <div className="bg-gray-50 px-4 py-3 rounded-t-lg border-b">
                        <h3 className="font-bold text-gray-700">Execution Log</h3>
                    </div>
                    <div className="flex-1 p-4 overflow-y-auto">
                        {isLegacy ? (
                            <LegacyLogBlock content={run.log_content || 'Waiting for logs...'} showRaw={showRaw} />
                        ) : entries.length > 0 ? (
                            entries.map((entry) => (
                                <LogEntry key={entry.id} entry={entry} showRaw={showRaw} />
                            ))
                        ) : (
                            <pre className="text-xs font-mono text-green-400 bg-gray-900 p-4 rounded whitespace-pre-wrap">
                                {run.log_content || 'Waiting for logs...'}
                            </pre>
                        )}
                    </div>
                </div>
            </div>
        </div>
    );
};
