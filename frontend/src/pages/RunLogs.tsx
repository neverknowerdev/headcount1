import React, { useState, useEffect } from 'react';

export const SessionLogs: React.FC<{logs: any[]}> = ({logs}) => {
    // Group logs by run_id
    const grouped = logs.reduce((acc, log) => {
        if (!acc[log.run_id]) acc[log.run_id] = [];
        acc[log.run_id].push(log);
        return acc;
    }, {} as Record<number, any[]>);

    return (
        <div className="space-y-4">
            {Object.entries(grouped).map(([runId, runLogs]) => (
                <div key={runId} className="bg-gray-800 rounded-lg overflow-hidden">
                    <div className="bg-gray-700 px-4 py-2 flex justify-between items-center cursor-pointer">
                        <span className="text-gray-200 font-semibold text-sm">Session #{runId}</span>
                        <span className="text-xs bg-gray-600 px-2 py-1 rounded text-gray-300">{(runLogs as any[]).length} events</span>
                    </div>
                    <div className="p-4 text-green-400 font-mono text-sm max-h-96 overflow-y-auto whitespace-pre-wrap">
                        {(runLogs as any[]).map((log: any, i: number) => (
                            <div key={i} className="mb-1 border-b border-gray-700 pb-1">
                                <span className="text-gray-300">{log.line}</span>
                            </div>
                        ))}
                    </div>
                </div>
            ))}
        </div>
    );
};

export const RunLogs: React.FC = () => {
  const [logs, setLogs] = useState<{run_id: number, line: string}[]>([]);

  useEffect(() => {
    const ws = new WebSocket(`ws://${window.location.host}/api/ws`);
    ws.onmessage = (event) => {
      const msg = JSON.parse(event.data);
      if (msg.type === 'run_log') {
        setLogs(prev => [...prev, msg.payload].slice(-500));
      }
    };
    return () => ws.close();
  }, []);

  return (
    <div className="h-full flex flex-col">
      <h1 className="text-2xl font-bold mb-6">Live Run Logs</h1>
      <div className="flex-1 bg-gray-900 p-4 rounded-lg overflow-y-auto shadow-inner">
        {logs.length === 0 ? (
          <div className="text-gray-500 italic flex items-center justify-center h-full font-mono text-sm">Waiting for agents to start executing tasks...</div>
        ) : (
          <SessionLogs logs={logs} />
        )}
      </div>
    </div>
  );
};
