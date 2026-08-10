/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams } from 'react-router-dom';
import { useStore } from '../store';

export const AgentManager: React.FC = () => {
    const { shortName } = useParams<{shortName: string}>();
    const { selectedCompanyId } = useStore();
    const [agents, setAgents] = useState<any[]>([]);
    const [builtinConfigs, setBuiltinConfigs] = useState<any[]>([]);
    const [builtinExpanded, setBuiltinExpanded] = useState(false);
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
        try {
            const cfgRes = await axios.get('/api/agent-configs');
            setBuiltinConfigs(cfgRes.data || []);
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

            {builtinConfigs.length > 0 && (
                <div className="border rounded-lg bg-gray-50" data-testid="builtin-agents">
                    <button
                        onClick={() => setBuiltinExpanded(v => !v)}
                        className="w-full flex items-center justify-between px-4 py-3 text-left hover:bg-gray-100 transition-colors rounded-lg"
                    >
                        <div className="flex items-center gap-2">
                            <span className="font-semibold text-gray-700">Role templates</span>
                            <span className="text-xs bg-violet-100 text-violet-700 px-2 py-0.5 rounded-full">{builtinConfigs.length}</span>
                            <span className="text-xs text-gray-400">Read-only defaults; runtime settings come from database agents</span>
                        </div>
                        <span className="text-gray-400 text-sm">{builtinExpanded ? '▾' : '▸'}</span>
                    </button>
                    {builtinExpanded && (
                        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 p-4 pt-1">
                            {builtinConfigs.map(cfg => (
                                <div key={cfg.name} className="bg-white p-4 rounded-lg border shadow-sm flex flex-col gap-2">
                                    <div className="flex justify-between items-start gap-2">
                                        <h3 className="text-sm font-bold text-gray-900">{cfg.name}</h3>
                                        <span className="bg-violet-100 text-violet-800 text-xs px-2 py-0.5 rounded-full shrink-0">template</span>
                                    </div>
                                    {cfg.description && <p className="text-xs text-gray-600">{cfg.description}</p>}
                                    <div className="flex flex-wrap gap-1 text-xs">
                                        {cfg.reasoning_level && (
                                            <span className="bg-purple-50 text-purple-700 px-1.5 py-0.5 rounded">reasoning: {cfg.reasoning_level}</span>
                                        )}
                                        {cfg.parent_agent && (
                                            <span className="bg-gray-100 text-gray-600 px-1.5 py-0.5 rounded">reports to {cfg.parent_agent}</span>
                                        )}
                                    </div>
                                    {cfg.subagents?.length > 0 && (
                                        <div className="text-xs text-gray-500">
                                            <span className="font-medium text-gray-600">Delegates to:</span> {cfg.subagents.join(', ')}
                                        </div>
                                    )}
                                    <details className="mt-auto">
                                        <summary className="text-xs text-indigo-600 cursor-pointer hover:underline">System prompt</summary>
                                        <div className="mt-1 text-xs text-gray-700 bg-gray-50 p-2 rounded border overflow-y-auto max-h-40 whitespace-pre-wrap font-mono">
                                            {cfg.prompt}
                                        </div>
                                    </details>
                                </div>
                            ))}
                        </div>
                    )}
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
