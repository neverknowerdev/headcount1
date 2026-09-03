/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams } from 'react-router-dom';
import { useStore } from '../store';
import { ChevronDown, ChevronRight, Trash2 } from 'lucide-react';

type AgentTemplate = {
    name: string;
    canonical_name?: string;
    slug?: string;
    description: string;
    prompt: string;
    best_models?: string[];
    allowed_tools?: string[];
    permissions?: string;
};

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
    const [templates, setTemplates] = useState<AgentTemplate[]>([]);
    const [showModal, setShowModal] = useState(false);
    const [selectedTemplate, setSelectedTemplate] = useState('');
    const [form, setForm] = useState({ name: '', description: '', system_prompt: '', permissions: '{}' });
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

    const fetchTemplates = useCallback(async () => {
        try {
            const res = await axios.get('/api/agent-configs');
            setTemplates(res.data || []);
        } catch (e) {
            console.error(e);
        }
    }, []);

    useEffect(() => {
        // eslint-disable-next-line react-hooks/set-state-in-effect
        fetchAgents();
    }, [fetchAgents]);

    useEffect(() => {
        // eslint-disable-next-line react-hooks/set-state-in-effect
        fetchTemplates();
    }, [fetchTemplates]);

    const openModal = () => {
        setForm({ name: '', description: '', system_prompt: '', permissions: '{}' });
        setSelectedTemplate('');
        setError('');
        setShowModal(true);
    };

    const selectTemplate = (name: string) => {
        setSelectedTemplate(name);
        const template = templates.find(item => item.name === name);
        if (!template) {
            setForm(current => ({ ...current, system_prompt: '', permissions: '{}' }));
            return;
        }
        setForm(current => ({
            ...current,
            system_prompt: template.prompt || '',
            permissions: template.permissions || '{}',
        }));
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
                permissions: form.permissions,
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
        const template = templates.find(item =>
            item.canonical_name === agent.role_key || item.name === agent.role_key || item.name === agent.name
        );
        const allowedTools = template?.allowed_tools || [];
        const canonicalName = agent.role_key || template?.canonical_name || agent.name;
        const slug = agent.short_name || template?.slug || '—';
        return (
            <div key={agent.id} data-testid={`builtin-agent-${agent.id}`} className={`bg-white rounded-lg border shadow-sm ${agent.enabled === false ? 'opacity-60' : ''}`}>
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
                    <AgentToggle agent={agent} onToggle={toggleAgent} />
                </div>
                {expanded && (
                    <div className="border-t px-5 py-4 space-y-4 text-sm">
                        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs">
                            <div><span className="block font-semibold uppercase tracking-wide text-gray-400">Canonical system name</span><code className="text-gray-800">{canonicalName}</code></div>
                            <div><span className="block font-semibold uppercase tracking-wide text-gray-400">Agent slug</span><code className="text-gray-800">{slug}</code></div>
                        </div>
                        <div>
                            <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">System prompt</p>
                            <div className="text-xs text-gray-700 bg-gray-50 p-3 rounded border whitespace-pre-wrap font-mono max-h-48 overflow-y-auto">{agent.system_prompt}</div>
                        </div>
                        <div>
                            <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">Available tools</p>
                            <div className="flex flex-wrap gap-1.5">
                                {allowedTools.length > 0 ? allowedTools.map(tool => <code key={tool} className="rounded bg-gray-100 px-2 py-1 text-xs text-gray-700">{tool}</code>) : <span className="text-xs text-gray-500">All tools</span>}
                            </div>
                        </div>
                        <button type="button" onClick={() => window.location.href = `/companies/${shortName}/agents/${agent.id}`} className="text-sm font-medium text-indigo-600 hover:text-indigo-800">Open edit page →</button>
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
                <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">System Prompt</p>
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
                        <span className="text-xs text-gray-400">Protected defaults; enable or disable them as needed</span>
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
                            <label className="block text-sm font-medium text-gray-700 mb-1" htmlFor="agent-template">Template</label>
                            <select
                                id="agent-template"
                                data-testid="agent-template"
                                className="w-full border rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                                value={selectedTemplate}
                                onChange={e => selectTemplate(e.target.value)}
                            >
                                <option value="">Blank agent</option>
                                {templates.map(template => (
                                    <option key={template.name} value={template.name}>{template.name}</option>
                                ))}
                            </select>
                            {selectedTemplate && (
                                <p className="mt-1 text-xs text-gray-500">
                                    Copied the template prompt and {templates.find(template => template.name === selectedTemplate)?.allowed_tools?.length || 0} tool settings. You can edit the prompt below.
                                </p>
                            )}
                        </div>

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
                            <label className="block text-sm font-medium text-gray-700 mb-1" htmlFor="agent-system-prompt">System prompt</label>
                            <textarea
                                id="agent-system-prompt"
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
