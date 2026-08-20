/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams } from 'react-router-dom';
import { useStore } from '../store';

export const AgentManager: React.FC = () => {
    const { shortName } = useParams<{shortName: string}>();
    const { selectedCompanyId } = useStore();
    const [agents, setAgents] = useState<any[]>([]);
    const [showModal, setShowModal] = useState(false);
    const [form, setForm] = useState({ name: '', description: '', system_prompt: '' });
    const [saving, setSaving] = useState(false);
    const [error, setError] = useState('');

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

    const openModal = () => {
        setForm({ name: '', description: '', system_prompt: '' });
        setError('');
        setShowModal(true);
    };

    const handleCreate = async () => {
        if (!form.name.trim()) { setError('Name is required.'); return; }
        setSaving(true);
        setError('');
        try {
            const res = await axios.post('/api/agents', {
                company_id: selectedCompanyId,
                name: form.name.trim(),
                description: form.description.trim(),
                system_prompt: form.system_prompt.trim(),
            });
            setShowModal(false);
            window.location.href = `/companies/${shortName}/agents/${res.data.id}`;
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to create agent.');
            setSaving(false);
        }
    };

    const toggleAgent = async (agent: any) => {
        const enabled = agent.enabled === false;
        try {
            const res = await axios.put(`/api/agents/${agent.id}`, { enabled });
            setAgents(current => current.map(item => item.id === agent.id ? res.data : item));
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to update agent.');
        }
    };

    const renderAgentCard = (agent: any) => (
        <div key={agent.id} className={`bg-white p-6 rounded-lg border shadow-sm flex flex-col ${agent.enabled === false ? 'opacity-60' : ''}`}>
            <div className="flex justify-between items-start mb-4 gap-3">
                <h3 className="text-lg font-bold text-gray-900 cursor-pointer hover:text-indigo-600" onClick={() => window.location.href=`/companies/${shortName}/agents/${agent.id}`}>{agent.name}</h3>
                <div className="flex items-center gap-2 shrink-0">
                    {agent.builtin && <span className="bg-violet-100 text-violet-800 text-xs px-2 py-1 rounded-full">Built-in</span>}
                    <span className="bg-indigo-100 text-indigo-800 text-xs px-2 py-1 rounded-full">{agent.model || 'Default Model'}</span>
                </div>
            </div>
            {agent.description && <p className="text-sm text-gray-600 mb-4">{agent.description}</p>}
            <div className="mt-auto">
                <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">System Prompt</p>
                <div className="text-xs text-gray-700 bg-gray-50 p-3 rounded border overflow-y-auto h-32 whitespace-pre-wrap font-mono">
                    {agent.system_prompt}
                </div>
            </div>
            <button
                onClick={() => toggleAgent(agent)}
                className={`mt-4 px-3 py-2 text-sm font-medium rounded-lg transition-colors ${agent.enabled === false ? 'bg-gray-200 text-gray-700 hover:bg-gray-300' : 'bg-green-100 text-green-800 hover:bg-green-200'}`}
            >
                {agent.enabled === false ? 'Enable agent' : 'Disable agent'}
            </button>
        </div>
    );

    const builtinAgents = agents.filter(agent => agent.builtin);
    const customAgents = agents.filter(agent => !agent.builtin);

    return (
        <div className="h-full flex flex-col space-y-6">
            <div className="flex items-center justify-between">
                <h1 className="text-2xl font-bold">Agents</h1>
                <button
                    onClick={openModal}
                    className="px-4 py-2 bg-indigo-600 text-white text-sm font-medium rounded-lg hover:bg-indigo-700 transition-colors"
                >
                    + Add agent
                </button>
            </div>

            {builtinAgents.length > 0 && (
                <div className="border rounded-lg bg-gray-50 p-4" data-testid="builtin-agents">
                    <div className="flex items-center gap-2 mb-4">
                        <span className="font-semibold text-gray-700">Built-in agents</span>
                        <span className="text-xs bg-violet-100 text-violet-700 px-2 py-0.5 rounded-full">{builtinAgents.length}</span>
                        <span className="text-xs text-gray-400">Protected defaults; enable or disable them as needed</span>
                    </div>
                    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
                        {builtinAgents.map(renderAgentCard)}
                    </div>
                </div>
            )}

            {customAgents.length > 0 && (
                <div>
                    <h2 className="text-lg font-semibold text-gray-800 mb-4">Custom agents</h2>
                    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
                        {customAgents.map(renderAgentCard)}
                    </div>
                </div>
            )}
            {agents.length === 0 && (
                <div className="text-center mt-16">
                    <p className="text-gray-400 italic mb-4">No agents hired yet.</p>
                    <button
                        onClick={openModal}
                        className="px-5 py-2.5 bg-indigo-600 text-white text-sm font-medium rounded-lg hover:bg-indigo-700 transition-colors"
                    >
                        + Add your first agent
                    </button>
                </div>
            )}

            {showModal && (
                <div className="fixed inset-0 bg-black/40 flex items-center justify-center z-50 p-4">
                    <div className="bg-white rounded-xl shadow-xl w-full max-w-md p-6 space-y-4">
                        <h2 className="text-lg font-semibold text-gray-900">New agent</h2>

                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Name <span className="text-red-500">*</span></label>
                            <input
                                autoFocus
                                className="w-full border rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                                placeholder="e.g. Research Assistant"
                                value={form.name}
                                onChange={e => setForm(f => ({ ...f, name: e.target.value }))}
                                onKeyDown={e => e.key === 'Enter' && handleCreate()}
                            />
                        </div>

                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
                            <input
                                className="w-full border rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                                placeholder="What does this agent do?"
                                value={form.description}
                                onChange={e => setForm(f => ({ ...f, description: e.target.value }))}
                            />
                        </div>

                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">System prompt</label>
                            <textarea
                                className="w-full border rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500 resize-none"
                                rows={4}
                                placeholder="Optional — you can set this later in agent settings."
                                value={form.system_prompt}
                                onChange={e => setForm(f => ({ ...f, system_prompt: e.target.value }))}
                            />
                        </div>

                        {error && <p className="text-sm text-red-600">{error}</p>}

                        <div className="flex justify-end gap-3 pt-1">
                            <button
                                onClick={() => setShowModal(false)}
                                className="px-4 py-2 text-sm text-gray-600 hover:text-gray-900"
                            >
                                Cancel
                            </button>
                            <button
                                onClick={handleCreate}
                                disabled={saving}
                                className="px-4 py-2 bg-indigo-600 text-white text-sm font-medium rounded-lg hover:bg-indigo-700 disabled:opacity-50 transition-colors"
                            >
                                {saving ? 'Creating…' : 'Create agent'}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};
