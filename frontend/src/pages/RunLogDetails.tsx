import React, { useState, useEffect, useRef, useCallback } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { ArrowLeft, Square, AlertCircle, RotateCcw } from 'lucide-react';
import { RunLogViewer, type AgentTokenStats } from '../components/RunLogViewer';
import { useWebSocket, wsUrl } from '../useWebSocket';
import { buildAgentStats } from '../utils/runStats';
import { parseLogContent } from '../utils/runLogParser';

import { mergeSnapshotWithLiveTail, sortBySeq } from '../utils/logMerge';

export const RunLogDetails: React.FC = () => {
    const { shortName, id } = useParams<{shortName: string, id: string}>();
    const [run, setRun] = useState<any>(null);
    const [isStopping, setIsStopping] = useState(false);
    const [isRerunning, setIsRerunning] = useState(false);
    const [logMessages, setLogMessages] = useState<any[]>([]);
    const [streamStalled, setStreamStalled] = useState<{at: number, message: string} | null>(null);
    const lastEventAtRef = useRef<number>(Date.now());
    const [tokenStats, setTokenStats] = useState<any>(null);
    const [agentStats, setAgentStats] = useState<AgentTokenStats[] | undefined>(undefined);

    // Per-agent breakdown across the whole session tree (children and
    // grandchildren); refreshed when child sessions end so the numbers stay
    // current while the run is live.
    const fetchAgentStats = async (rootRun: any) => {
        try {
            const chRes = await axios.get(`/api/runs/${id}/children?deep=true`);
            const children = chRes.data || [];
            setAgentStats(children.length > 0 ? buildAgentStats(rootRun, children) : undefined);
        } catch (e) {
            console.error('failed to load child runs', e);
        }
    };

    const fetchRun = useCallback(async () => {
            try {
                const res = await axios.get(`/api/runs/${id}`);
                setRun(res.data);
                setTokenStats(res.data?.token_stats || null);
                fetchAgentStats(res.data);

                let messages: any[];
                if (Array.isArray(res.data?.log_entries) && res.data.log_entries.length > 0) {
                    messages = sortBySeq(res.data.log_entries.map((entry: any, i: number) => ({ id: i, entry })));
                } else {
                    messages = parseLogContent(res.data?.log_content || '');
                }

                // For failed runs with no error entries, extract errors from log_content
                if (res.data?.status === 'failed') {
                    const hasError = messages.some((m: any) => m.entry.type === 'error');
                    if (!hasError) {
                        const logContent: string = res.data?.log_content || '';
                        if (logContent) {
                            const lines = logContent.split('\n').filter((l: string) => l.trim());
                            const errorLines = lines.filter((l: string) =>
                                /\b(error|Error|FAIL|failed|panic|exception|fatal)\b/.test(l)
                            );
                            const contextLines = errorLines.length > 0 ? errorLines : lines.slice(-15);
                            if (contextLines.length > 0) {
                                messages = [...messages, {
                                    id: messages.length,
                                    entry: {
                                        type: 'error',
                                        content: errorLines.length > 0
                                            ? contextLines.join('\n')
                                            : `Run failed. Last log output:\n${contextLines.join('\n')}`,
                                    },
                                }];
                            }
                        }
                    }
                }

                setLogMessages(prev => mergeSnapshotWithLiveTail(messages, prev));
            } catch (e) {
                console.error(e);
            }
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [id]);

    // Navigating to a different run must start from a clean slate — the
    // snapshot merge below deliberately preserves on-screen entries, which
    // would leak the previous run's tail into the new one.
    useEffect(() => {
        setLogMessages([]);
        setRun(null);
        setStreamStalled(null);
        lastEventAtRef.current = Date.now();
    }, [id]);

    useEffect(() => {
        fetchRun();

        // Client-side fallback: if no events for 45s while run is "running",
        // surface a "stream stalled" warning even before the server detects it.
        const stallCheck = setInterval(() => {
            if (run?.status === 'running' && Date.now() - lastEventAtRef.current > 45000) {
                setStreamStalled(prev => prev ?? { at: lastEventAtRef.current, message: 'No log activity for 45+ seconds' });
            }
        }, 5000);

        return () => {
            clearInterval(stallCheck);
        };
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [fetchRun, run?.status]);

    const runIdInt = parseInt(id || '0');
    useWebSocket(wsUrl(), (msg) => {
        if (msg.type === 'run_log' && msg.payload.run_id === runIdInt) {
            lastEventAtRef.current = Date.now();
            setStreamStalled(null);
            if (msg.payload.entry) {
                setLogMessages(prev => [...prev, { id: prev.length, entry: msg.payload.entry }]);
            } else if (msg.payload.line) {
                setLogMessages(prev => [...prev, { id: prev.length, entry: { type: 'info', content: msg.payload.line } }]);
            }
        } else if (msg.type === 'run_ended' && msg.payload.run_id === runIdInt) {
            setRun((prev: any) => prev ? { ...prev, status: msg.payload.status } : prev);
        } else if (msg.type === 'run_ended' && run) {
            // Another run ended — likely one of our child sessions. Refresh
            // the per-agent token breakdown.
            fetchAgentStats(run);
        } else if (msg.type === 'run_status' && msg.payload.run_id === runIdInt) {
            lastEventAtRef.current = Date.now();
            setRun((prev: any) => prev ? { ...prev, latest_reported_status: msg.payload.status } : prev);
        } else if (msg.type === 'run_stalled' && msg.payload.run_id === runIdInt) {
            lastEventAtRef.current = Date.now();
            setStreamStalled({ at: Date.now(), message: msg.payload.message || 'Stream stalled' });
        }
    }, {
        // Re-fetch the full run on every (re)connect: the log stream is
        // rebuilt from the database, so log lines missed while disconnected
        // are recovered rather than lost.
        onConnect: fetchRun,
    });

    const handleStopRun = async () => {
        if (!id || !window.confirm('Are you sure you want to stop this run?')) return;
        setIsStopping(true);
        try {
            await axios.post(`/api/runs/${id}/stop`);
        } catch (e) {
            console.error(e);
            alert('Failed to stop run');
        } finally {
            setIsStopping(false);
        }
    };

    const handleRerun = async () => {
        if (!run?.task_id) return;
        setIsRerunning(true);
        try {
            await axios.post(`/api/tasks/${run.task_id}/rerun`);
        } catch (e) {
            console.error(e);
            alert('Failed to start re-run');
        } finally {
            setIsRerunning(false);
        }
    };

    if (!run) return <div>Loading...</div>;

    return (
        <div className="h-full flex flex-col">
            <div className="mb-6 flex items-center justify-between">
                <div className="flex items-center space-x-4">
                    <Link to={`/companies/${shortName}/runs`} className="text-gray-500 hover:text-gray-900">
                        <ArrowLeft size={20} />
                    </Link>
                    <h1 className="text-2xl font-bold">Run {run.name || `#${run.id}`} Details</h1>
                </div>
                <div className="flex items-center gap-2">
                    {run.status === 'running' && (
                        <button
                            onClick={handleStopRun}
                            disabled={isStopping}
                            className="flex items-center gap-2 bg-red-600 text-white px-4 py-2 rounded hover:bg-red-700 disabled:opacity-50 disabled:cursor-not-allowed"
                        >
                            <Square size={16} />
                            {isStopping ? 'Stopping...' : 'Stop Run'}
                        </button>
                    )}
                    {run.is_latest && run.status !== 'running' && (
                        <button
                            onClick={handleRerun}
                            disabled={isRerunning}
                            className="flex items-center gap-2 bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 disabled:opacity-50 disabled:cursor-not-allowed"
                        >
                            <RotateCcw size={16} />
                            {isRerunning ? 'Starting...' : 'Re-run'}
                        </button>
                    )}
                </div>
            </div>

            {streamStalled && run.status === 'running' && (
                <div className="mb-4 flex items-start gap-2 bg-amber-50 border border-amber-300 text-amber-900 px-3 py-2 rounded">
                    <AlertCircle size={16} className="mt-0.5 shrink-0" />
                    <div className="text-sm">
                        <div className="font-medium">Stream stalled</div>
                        <div className="text-amber-800">{streamStalled.message}. The run may still be working, or it may have died. If no new activity appears, consider stopping the run.</div>
                    </div>
                </div>
            )}

            <div className="grid grid-cols-3 gap-6 flex-1 min-h-0">
                <div className="col-span-1 bg-white p-6 rounded-lg shadow border space-y-4">
                    <h3 className="font-bold text-lg border-b pb-2">Context</h3>
                    <div>
                        <p className="text-sm text-gray-500">Status</p>
                        <p className="font-medium capitalize">{run.status}</p>
                    </div>
                    {run.latest_reported_status && (
                        <div>
                            <p className="text-sm text-gray-500">Current Activity</p>
                            <p className="font-medium text-violet-700" data-testid="run-current-status">{run.latest_reported_status}</p>
                        </div>
                    )}
                    <div>
                        <p className="text-sm text-gray-500">Agent</p>
                        <p className="font-medium">{run.agent?.name}</p>
                    </div>
                    {run.parent_run_id && (
                        <div>
                            <p className="text-sm text-gray-500">Parent Session</p>
                            <Link to={`/companies/${shortName}/run-logs/${run.parent_run_id}`} className="font-medium text-violet-600 hover:underline">
                                Run #{run.parent_run_id}
                            </Link>
                        </div>
                    )}
                    <div>
                        <p className="text-sm text-gray-500">Task</p>
                        <Link to={`/companies/${shortName}/tasks`} className="font-medium text-indigo-600 hover:underline">{run.task?.title} ({run.task?.ref_key || `#${run.task_id}`})</Link>
                    </div>
                    <div>
                        <p className="text-sm text-gray-500">Started At</p>
                        <p className="font-medium">{(() => { const d = new Date(run.started_at); return d.getFullYear() > 1 ? d.toLocaleString() : (run.ended_at ? new Date(run.ended_at).toLocaleString() : '...'); })()}</p>
                    </div>
                </div>

                <div className="col-span-2 bg-gray-50 rounded-lg shadow border flex flex-col min-h-0">
                    <RunLogViewer messages={logMessages} status={run.status} tokenStats={tokenStats} agentStats={agentStats} runId={run.id} />
                </div>
            </div>
        </div>
    );
};
