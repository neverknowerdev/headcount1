/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams } from 'react-router-dom';
import { useStore } from '../store';
import { ChevronDown, ChevronRight, Trash2 } from 'lucide-react';

const AgentToggle: React.FC<{ agent: any; onToggle: (agent: any) => void }> = ({ agent, onToggle }) => {
    const action = agent.enabled === false ? 'Enable' : 'Disable';
    return (
        <label className="relative inline-flex items-center cursor-pointer shrink-0" title={`${action} agent`}>
            <input
                type="checkbox"
                role="switch"
                aria-label={`${action} ${agent.name}`}
                checked={agent.enabled !== false}
                onChange={() => onToggle(agent)}
                className="sr-only peer"
            />
            <span className="w-9 h-5 bg-gray-300 rounded-full peer peer-checked:bg-green-500 after:content-[''] after:absolute after:top-0.5 after:left-0.5 after:bg-white after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-4" />
        </label>
    );
};

export const AgentManager: React.FC = () => {
    const { shortName } = useParams<{shortName: string}>();
    const { selectedCompanyId } = useStore();
    const [agents, setAgents] = useState<any[]>([]);
    const [showModal, setShowModal] = useState(false);
    const [form, setForm] = useState({ name: '', description: '', system_prompt: '' });
    const [saving, setSaving] = useState(false);
    const [expandedBuiltins, setExpandedBuiltins] = useState<Record<number, boolean>>({});
    const [deletingAgentId, setDeletingAgentId] = useState<number | null>(null);
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
                // A role's prompt says who is speaking; one line is enough.
                system_prompt: form.system_prompt.trim() || `You are the ${form.name.trim()} agent.`,
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

    const deleteAgent = async (agent: any) => {
        if (agent.builtin || !window.confirm(`Delete ${agent.name}? This cannot be undone.`)) return;
        setDeletingAgentId(agent.id);
        setError('');
        try {
            await axios.delete(`/api/agents/${agent.id}`);
            setAgents(current => current.filter(item => item.id !== agent.id));
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to delete agent.');
        } finally {
            setDeletingAgentId(null);
        }
    };

    const renderBuiltinCard = (agent: any) => {
        const expanded = !!expandedBuiltins[agent.id];
        return (
            <div key={agent.id} data-testid={`builtin-agent-${agent.id}`} className="bg-white rounded-lg border shadow-sm">
                <div className="flex items-center gap-2 p-4">
                    <button
                        type="button"
                        aria-expanded={expanded}
                        aria-label={`${expanded ? 'Collapse' : 'Expand'} ${agent.name}`}
                        onClick={() => setExpandedBuiltins(current => ({ ...current, [agent.id]: !expanded }))}
                        className="text-gray-500 hover:text-indigo-600 shrink-0"
                    >
                        {expanded ? <ChevronDown size={18} /> : <ChevronRight size={18} />}
                    </button>
                    <button
                        type="button"
                        onClick={() => setExpandedBuiltins(current => ({ ...current, [agent.id]: !expanded }))}
                        className="min-w-0 flex-1 text-left"
                    >
                        <span className="block text-base font-bold text-gray-900 break-words">{agent.name}</span>
                        <span className="block text-xs text-gray-500 line-clamp-2">{agent.description}</span>
                    </button>
                    <span className="shrink-0 rounded-full bg-green-100 px-2 py-1 text-xs font-medium text-green-700">Always enabled</span>
                </div>
                {expanded && (
                    <div className="border-t px-5 py-4 space-y-4 text-sm">
                        <div>
                            <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">Prompt</p>
                            <div className="text-xs text-gray-700 bg-gray-50 p-3 rounded border whitespace-pre-wrap font-mono max-h-48 overflow-y-auto">{agent.system_prompt}</div>
                        </div>
                        <button type="button" onClick={() => window.location.href = `/companies/${shortName}/agents/${agent.id}`} className="text-sm font-medium text-indigo-600 hover:text-indigo-800">Edit prompt, see usage →</button>
                    </div>
                )}
            </div>
        );
    };

    const renderCustomAgentCard = (agent: any) => (
        <div key={agent.id} className={`bg-white p-6 rounded-lg border shadow-sm flex flex-col ${agent.enabled === false ? 'opacity-60' : ''}`}>
            <div className="flex items-start gap-3 mb-4">
                <button type="button" className="flex-1 min-w-0 text-left text-lg font-bold text-gray-900 break-words hover:text-indigo-600" onClick={() => window.location.href=`/companies/${shortName}/agents/${agent.id}`}>{agent.name}</button>
                <AgentToggle agent={agent} onToggle={toggleAgent} />
            </div>
            {agent.description && <p className="text-sm text-gray-600 mb-4">{agent.description}</p>}
            <div className="mt-auto">
                <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">Prompt</p>
                <div className="text-xs text-gray-700 bg-gray-50 p-3 rounded border overflow-y-auto h-32 whitespace-pre-wrap font-mono">{agent.system_prompt}</div>
            </div>
            <div className="mt-4 flex items-center justify-end">
                <button type="button" onClick={() => deleteAgent(agent)} disabled={deletingAgentId === agent.id} aria-label={`Delete ${agent.name}`} title="Delete custom agent" className="px-3 py-2 text-red-600 border border-red-200 rounded-lg hover:bg-red-50 disabled:opacity-50">
                    <Trash2 size={16} />
                </button>
            </div>
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
                        <span className="text-xs text-gray-400">Roles the workflow speaks as. Their prompts are yours to extend.</span>
                    </div>
                    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
                        {builtinAgents.map(renderBuiltinCard)}
                    </div>
                </div>
            )}

            {customAgents.length > 0 && (
                <div>
                    <h2 className="text-lg font-semibold text-gray-800 mb-4">Custom agents</h2>
                    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
                        {customAgents.map(renderCustomAgentCard)}
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
                            <label className="block text-sm font-medium text-gray-700 mb-1" htmlFor="agent-name">Name <span className="text-red-500">*</span></label>
                            <input
                                id="agent-name"
                                autoFocus
                                className="w-full border rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                                placeholder="e.g. Research Assistant"
                                value={form.name}
                                onChange={e => setForm(f => ({ ...f, name: e.target.value }))}
                                onKeyDown={e => e.key === 'Enter' && handleCreate()}
                            />
                        </div>

                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1" htmlFor="agent-description">Description</label>
                            <input
                                id="agent-description"
                                className="w-full border rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                                placeholder="What does this agent do?"
                                value={form.description}
                                onChange={e => setForm(f => ({ ...f, description: e.target.value }))}
                            />
                        </div>

                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1" htmlFor="agent-system-prompt">Prompt</label>
                            <textarea
                                id="agent-system-prompt"
                                className="w-full border rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500 resize-none"
                                rows={4}
                                placeholder="Optional — defaults to “You are the <name> agent.”"
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
