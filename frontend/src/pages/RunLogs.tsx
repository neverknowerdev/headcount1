import React, { useState, useEffect } from 'react';

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
      <div className="flex-1 bg-gray-900 text-green-400 p-4 rounded-lg overflow-y-auto font-mono text-sm shadow-inner whitespace-pre-wrap">
        {logs.length === 0 ? (
          <div className="text-gray-500 italic flex items-center justify-center h-full">Waiting for agents to start executing tasks...</div>
        ) : (
          logs.map((log, i) => (
            <div key={i} className="mb-1 border-b border-gray-800 pb-1">
              <span className="text-indigo-400 mr-3 select-none">[{log.run_id}]</span>
              <span className="text-gray-300">{log.line}</span>
            </div>
          ))
        )}
      </div>
    </div>
  );
};
