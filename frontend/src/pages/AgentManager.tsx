/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams } from 'react-router-dom';
import { useStore } from '../store';

export const AgentManager: React.FC = () => {
    const { shortName } = useParams<{shortName: string}>();
    const { selectedCompanyId } = useStore();
    const [agents, setAgents] = useState<any[]>([]);

    const fetchAgents = useCallback(async () => {
        if (!selectedCompanyId) return;
        try {
            const res = await axios.get(`/api/agents?company_id=${selectedCompanyId}`);
            setAgents(res.data || []);
        } catch (e) {
            console.error(e);
        }
    }, [selectedCompanyId]);

    useEffect(() => {
        // eslint-disable-next-line react-hooks/set-state-in-effect
        fetchAgents();
    }, [fetchAgents]);

    return (
        <div className="h-full flex flex-col space-y-6">
            <h1 className="text-2xl font-bold">Agents</h1>

            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
                {agents.map(agent => (
                    <div key={agent.id} className="bg-white p-6 rounded-lg border shadow-sm flex flex-col">
                        <div className="flex justify-between items-start mb-4">
                            <h3 className="text-lg font-bold text-gray-900 cursor-pointer hover:text-indigo-600" onClick={() => window.location.href=`/companies/${shortName}/agents/${agent.id}`}>{agent.name}</h3>
                            <span className="bg-indigo-100 text-indigo-800 text-xs px-2 py-1 rounded-full">{agent.model || 'Default Model'}</span>
                        </div>
                        {agent.description && <p className="text-sm text-gray-600 mb-4">{agent.description}</p>}

                        <div className="mt-auto">
                            <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">System Prompt</p>
                            <div className="text-xs text-gray-700 bg-gray-50 p-3 rounded border overflow-y-auto h-32 whitespace-pre-wrap font-mono">
                                {agent.system_prompt}
                            </div>
                        </div>
                    </div>
                ))}
            </div>
            {agents.length === 0 && (
                <div className="text-center text-gray-500 italic mt-10">No agents hired yet.</div>
            )}
        </div>
    );
};
