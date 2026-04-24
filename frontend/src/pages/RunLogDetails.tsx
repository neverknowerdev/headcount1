import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { ArrowLeft } from 'lucide-react';

export const RunLogDetails: React.FC = () => {
    const { shortName, id } = useParams<{shortName: string, id: string}>();
    const [run, setRun] = useState<any>(null);

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
            if (msg.type === 'run_log' && msg.payload.run_id === parseInt(id || '0')) {
                setRun((prev: any) => prev ? { ...prev, log_content: (prev.log_content || '') + msg.payload.line + '\n' } : prev);
            } else if (msg.type === 'run_ended' && msg.payload.run_id === parseInt(id || '0')) {
                setRun((prev: any) => prev ? { ...prev, status: msg.payload.status } : prev);
            }
        };
        return () => ws.close();
    }, [id]);

    if (!run) return <div>Loading...</div>;

    return (
        <div className="h-full flex flex-col">
            <div className="mb-6 flex items-center space-x-4">
                <Link to={`/companies/${shortName}/runs`} className="text-gray-500 hover:text-gray-900">
                    <ArrowLeft size={20} />
                </Link>
                <h1 className="text-2xl font-bold">Run #{run.id} Details</h1>
            </div>

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
                        <p className="font-medium">{new Date(run.created_at).toLocaleString()}</p>
                    </div>
                </div>

                <div className="col-span-2 bg-gray-900 rounded-lg shadow flex flex-col min-h-0">
                    <div className="bg-gray-800 px-4 py-2 rounded-t-lg border-b border-gray-700">
                        <h3 className="font-bold text-gray-200">Execution Log</h3>
                    </div>
                    <div className="flex-1 p-4 overflow-y-auto">
                        <pre className="text-xs font-mono text-green-400 whitespace-pre-wrap">
                            {run.log_content || 'Waiting for logs...'}
                        </pre>
                    </div>
                </div>
            </div>
        </div>
    );
};
