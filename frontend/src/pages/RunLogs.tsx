import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { useStore } from '../store';

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
                    const line = msg.payload.line;
                    return prev.map(r =>
                        r.id === runId
                            ? { ...r, log_content: (r.log_content || '') + line + '\n' }
                            : r
                    );
                });
            } else if (msg.type === 'run_started') {
                fetchRuns(); // refresh runs to get the new run
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
            <h1 className="text-2xl font-bold mb-6">Run Logs</h1>
            <div className="flex-1 bg-white p-6 rounded-lg shadow border overflow-y-auto">
                {runs.length === 0 ? (
                    <div className="text-gray-500 italic flex items-center justify-center h-full font-mono text-sm">No agent runs recorded yet...</div>
                ) : (
                    <div className="space-y-4">
                        {runs.map((r: any) => (
                            <details key={r.id} className="bg-gray-50 border rounded p-4 text-sm">
                                <summary className="font-semibold cursor-pointer text-indigo-700 flex justify-between items-center">
                                    <span>Run #{r.id} for Task #{r.task_id} by {r.agent?.name} ({r.status}) - {(() => { const d = new Date(r.started_at); return d.getFullYear() > 1 ? d.toLocaleString() : (r.ended_at ? new Date(r.ended_at).toLocaleString() : '...'); })()}</span>
                                    <Link to={`/companies/${shortName}/run-logs/${r.id}`} className="text-xs bg-indigo-100 text-indigo-700 px-2 py-1 rounded hover:bg-indigo-200 ml-4">
                                        View Details
                                    </Link>
                                </summary>
                                <pre className="mt-4 text-xs bg-gray-900 text-green-400 p-3 rounded overflow-x-auto whitespace-pre-wrap max-h-96">
                                    {r.log_content || 'Waiting for logs...'}
                                </pre>
                            </details>
                        ))}
                    </div>
                )}
            </div>
        </div>
    );
};
