import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { ArrowLeft, Square, AlertCircle } from 'lucide-react';
import { RunLogViewer } from '../components/RunLogViewer';

export const RunLogDetails: React.FC = () => {
    const { shortName, id } = useParams<{shortName: string, id: string}>();
    const [run, setRun] = useState<any>(null);
    const [isStopping, setIsStopping] = useState(false);
    const [logMessages, setLogMessages] = useState<any[]>([]);
    const [streamStalled, setStreamStalled] = useState<{at: number, message: string} | null>(null);
    const [lastEventAt, setLastEventAt] = useState<number>(Date.now());
    const [tokenStats, setTokenStats] = useState<any>(null);

    useEffect(() => {
        const fetchRun = async () => {
            try {
                const res = await axios.get(`/api/runs/${id}`);
                setRun(res.data);
                setTokenStats(res.data?.token_stats || null);

                // log_entries is already a parsed array from the backend
                if (Array.isArray(res.data?.log_entries) && res.data.log_entries.length > 0) {
                    setLogMessages(res.data.log_entries.map((entry: any, i: number) => ({
                        id: i,
                        entry: entry
                    })));
                } else {
                    parseLogContent(res.data?.log_content || '');
                }
            } catch (e) {
                console.error(e);
            }
        };
        
        const parseLogContent = (logContent: string) => {
            if (!logContent) return;
            const lines = logContent.split('\n').filter((l: string) => l.trim());
            const messages = [];
            let i = 0;
            for (const line of lines) {
                const trimmed = line.trim();
                // Try to detect JSON lines that are requests or responses
                if (trimmed.startsWith('{')) {
                    try {
                        const parsed = JSON.parse(trimmed);
                        // OpenCode request format: has "agent" and "parts"
                        if (parsed.agent && parsed.parts && Array.isArray(parsed.parts)) {
                            messages.push({
                                id: i++,
                                entry: { type: 'request', content: trimmed, model: parsed.model?.modelID || parsed.model }
                            });
                            continue;
                        }
                        // OpenCode response format: has "info" and "parts"
                        if (parsed.info && parsed.parts && Array.isArray(parsed.parts)) {
                            messages.push({
                                id: i++,
                                entry: { type: 'response', content: trimmed, status_code: 200 }
                            });
                            continue;
                        }
                        // LLM provider request format: has "messages" array
                        if (parsed.messages && Array.isArray(parsed.messages)) {
                            messages.push({
                                id: i++,
                                entry: { type: 'request', content: trimmed, model: parsed.model }
                            });
                            continue;
                        }
                        // LLM provider response format: has "choices" array
                        if (parsed.choices && Array.isArray(parsed.choices)) {
                            messages.push({
                                id: i++,
                                entry: { type: 'response', content: trimmed, status_code: 200 }
                            });
                            continue;
                        }
                        // Streaming response format: has "reasoning" or "tokens"
                        if (parsed.reasoning || parsed.tokens) {
                            messages.push({
                                id: i++,
                                entry: { type: 'response', content: trimmed, status_code: 200 }
                            });
                            continue;
                        }
                        // Non-streaming response format: { raw, reasoning, tokens }
                        if (parsed.raw || parsed.reasoning) {
                            messages.push({
                                id: i++,
                                entry: { type: 'response', content: trimmed, status_code: 200 }
                            });
                            continue;
                        }
                        // Tool response entry (engine-side: tool_result, tool, tool_response)
                        if (parsed.type === 'tool_response' || parsed.type === 'tool_result' || parsed.type === 'tool') {
                            messages.push({
                                id: i++,
                                entry: { type: 'tool_response', content: parsed.content || trimmed, tool_name: parsed.tool_name || parsed.name, output_tokens: parsed.output_tokens }
                            });
                            continue;
                        }
                    } catch {
                        // Not valid JSON, treat as info
                    }
                }
                // Regular info line
                messages.push({
                    id: i++,
                    entry: { type: 'info', content: trimmed }
                });
            }
            setLogMessages(messages);
        };

        fetchRun();

        const ws = new WebSocket(`ws://${window.location.host}/api/ws`);
        ws.onmessage = (event) => {
            const msg = JSON.parse(event.data);
            if (msg.type === 'run_log' && msg.payload.run_id === parseInt(id || '0')) {
                setLastEventAt(Date.now());
                setStreamStalled(null);
                if (msg.payload.entry) {
                    setLogMessages(prev => [...prev, { id: prev.length, entry: msg.payload.entry }]);
                } else if (msg.payload.line) {
                    setLogMessages(prev => [...prev, { id: prev.length, entry: { type: 'info', content: msg.payload.line } }]);
                }
            } else if (msg.type === 'run_ended' && msg.payload.run_id === parseInt(id || '0')) {
                setRun((prev: any) => prev ? { ...prev, status: msg.payload.status } : prev);
            } else if (msg.type === 'run_stalled' && msg.payload.run_id === parseInt(id || '0')) {
                setLastEventAt(Date.now());
                setStreamStalled({ at: Date.now(), message: msg.payload.message || 'Stream stalled' });
            }
        };

        // Client-side fallback: if no events for 45s while run is "running",
        // surface a "stream stalled" warning even before the server detects it.
        const stallCheck = setInterval(() => {
            if (run?.status === 'running' && Date.now() - lastEventAt > 45000) {
                setStreamStalled(prev => prev ?? { at: lastEventAt, message: 'No log activity for 45+ seconds' });
            }
        }, 5000);

        return () => {
            ws.close();
            clearInterval(stallCheck);
        };
    }, [id, run?.status, lastEventAt]);

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

    if (!run) return <div>Loading...</div>;

    return (
        <div className="h-full flex flex-col">
            <div className="mb-6 flex items-center justify-between">
                <div className="flex items-center space-x-4">
                    <Link to={`/companies/${shortName}/runs`} className="text-gray-500 hover:text-gray-900">
                        <ArrowLeft size={20} />
                    </Link>
                    <h1 className="text-2xl font-bold">Run #{run.id} Details</h1>
                </div>
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
                </div>

                <div className="col-span-2 bg-gray-50 rounded-lg shadow border flex flex-col min-h-0">
                    <RunLogViewer messages={logMessages} status={run.status} tokenStats={tokenStats} />
                </div>
            </div>
        </div>
    );
};
