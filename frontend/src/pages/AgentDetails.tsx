import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';

import { ArrowLeft, Save } from 'lucide-react';
import { useStore } from '../store';

export const AgentDetails: React.FC = () => {
    const { id, shortName } = useParams<{id: string, shortName: string}>();
    const { selectedCompanyId } = useStore();
    const [agent, setAgent] = useState<any>(null);
    const [stats, setStats] = useState<any>(null);
    const [providers, setProviders] = useState<any[]>([]);
    const [activeTab, setActiveTab] = useState('overview');
    const [runs, setRuns] = useState<any[]>([]);
    const [mcpServers, setMcpServers] = useState<any[]>([]);
    const [mcpAssignments, setMcpAssignments] = useState<Record<number, boolean>>({});

    const [formData, setFormData] = useState({ name: '', description: '', system_prompt: '', model: '', provider_id: '', mode: 'primary', permissions: '{}' });

    const fetchData = useCallback(async () => {
        try {
            const [agentRes, statsRes, provRes] = await Promise.all([
                axios.get(`/api/agents/${id}`),
                axios.get(`/api/agents/${id}/stats`),
                axios.get('/api/providers')
            ]);
            setAgent(agentRes.data);
            setStats(statsRes.data);
            setProviders(provRes.data || []);
            setFormData({
                name: agentRes.data.name,
                description: agentRes.data.description || '',
                system_prompt: agentRes.data.system_prompt,
                model: agentRes.data.model || '',
                provider_id: agentRes.data.provider_id?.toString() || '', mode: agentRes.data.mode || 'primary', permissions: agentRes.data.permissions || '{}'
            });
        } catch (e) {
            console.error(e);
        }
        try {
            const runsRes = await axios.get(`/api/agents/${id}/runs`);
            setRuns(runsRes.data || []);
        } catch (e) {
            console.error(e);
        }
    }, [id]);

    const fetchMCPData = useCallback(async () => {
        if (!id) return;
        try {
            const [serversRes, assignRes] = await Promise.all([
                axios.get('/api/mcp-servers'),
                axios.get(`/api/agents/${id}/mcp-servers`),
            ]);
            setMcpServers(serversRes.data || []);
            const map: Record<number, boolean> = {};
            for (const a of (assignRes.data || [])) {
                map[a.mcp_server_id] = a.enabled;
            }
            // Paperclip2 is always on — ensure it's always reflected as enabled
            const p2 = (serversRes.data || []).find((s: any) => s.name === 'paperclip2');
            if (p2) map[p2.id] = true;
            setMcpAssignments(map);
        } catch (e) {
            console.error(e);
        }
    }, [id]);

    useEffect(() => {
        fetchData();
    }, [fetchData]);

    useEffect(() => {
        if (activeTab === 'tools') fetchMCPData();
    }, [activeTab, fetchMCPData]);

    const handleSaveMCPAssignments = async () => {
        try {
            // Always include paperclip2 as enabled
            const p2 = mcpServers.find((s: any) => s.name === 'paperclip2');
            const assignments = Object.entries(mcpAssignments).map(([serverId, enabled]) => ({
                mcp_server_id: parseInt(serverId),
                enabled,
            }));
            if (p2 && !assignments.find(a => a.mcp_server_id === p2.id)) {
                assignments.push({ mcp_server_id: p2.id, enabled: true });
            }
            await axios.put(`/api/agents/${id}/mcp-servers`, assignments);
        } catch (e) {
            alert('Failed to save MCP assignments');
        }
    };

    const handleSave = async (e: React.FormEvent) => {
        e.preventDefault();
        try {
            const payload = {
                ...formData,
                provider_id: formData.provider_id ? parseInt(formData.provider_id) : null
            };
            await axios.put(`/api/agents/${id}`, payload);
            fetchData();
            alert('Saved successfully!');
        } catch (e) {
            console.error(e);
            alert('Failed to save');
        }
    };

    if (!agent) return <div>Loading...</div>;

    return (
        <div className="h-full flex flex-col">
            <div className="mb-6 flex items-center space-x-4">
                <Link to={`/companies/${shortName}/agents`} className="text-gray-500 hover:text-gray-900"><ArrowLeft size={20} /></Link>
                <h1 className="text-2xl font-bold">{agent.name}</h1>
            </div>

            <div className="border-b mb-6">
                <nav className="-mb-px flex space-x-8">
                    {['overview', 'logs', 'settings', 'tools'].map(tab => (
                        <button
                            key={tab}
                            onClick={() => setActiveTab(tab)}
                            className={`${activeTab === tab ? 'border-indigo-500 text-indigo-600' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'} whitespace-nowrap py-4 px-1 border-b-2 font-medium text-sm capitalize`}
                        >
                            {tab}
                        </button>
                    ))}
                </nav>
            </div>

            <div className="flex-1 overflow-y-auto">
                {activeTab === 'overview' && (
                    <div className="space-y-6">
                        <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
                            <div className="bg-white p-4 rounded-lg shadow border">
                                <p className="text-sm text-gray-500 mb-1">Total Requests</p>
                                <p className="text-2xl font-bold">{stats?.total_requests || 0}</p>
                            </div>
                            <div className="bg-white p-4 rounded-lg shadow border">
                                <p className="text-sm text-gray-500 mb-1">Total Tokens</p>
                                <p className="text-2xl font-bold">{stats?.total_tokens || 0}</p>
                            </div>
                            <div className="bg-white p-4 rounded-lg shadow border">
                                <p className="text-sm text-gray-500 mb-1">Prompt Tokens</p>
                                <p className="text-2xl font-bold">{stats?.prompt_tokens || 0}</p>
                            </div>
                            <div className="bg-white p-4 rounded-lg shadow border">
                                <p className="text-sm text-gray-500 mb-1">Completion Tokens</p>
                                <p className="text-2xl font-bold">{stats?.completion_tokens || 0}</p>
                            </div>
                        </div>

                        <div className="bg-white p-6 rounded-lg shadow border">
                            <h3 className="font-bold mb-4">Configuration</h3>
                            <div className="space-y-4">
                                <div>
                                    <p className="text-sm text-gray-500 mb-1">Active Provider</p>
                                    <p className="font-medium">{providers.find(p => p.id === agent.provider_id)?.name || 'None'}</p>
                                </div>
                                <div>
                                    <p className="text-sm text-gray-500 mb-1">Active Model</p>
                                    <p className="font-medium">{agent.model || 'None'}</p>
                                </div>
                                <div className="pt-2">
                                    <p className="text-sm text-gray-500 mb-2">Proxy URL for CLI</p>
                                    <p className="font-mono text-sm bg-gray-100 p-2 rounded break-all">
                                        {window.location.protocol}//{window.location.host}/api/proxy/agent/{agent.id}/v1/chat/completions
                                    </p>
                                </div>
                            </div>
                        </div>
                    </div>
                )}

                {activeTab === 'logs' && (
                    <div className="space-y-4">
                        {runs.length === 0 ? (
                            <p className="text-sm text-gray-500 italic">No runs yet.</p>
                        ) : (
                            runs.map((r: any) => (
                                <details key={r.id} className="bg-gray-50 border rounded p-4 text-sm">
                                    <summary className="font-semibold cursor-pointer text-indigo-700">Run #{r.id} for Task #{r.task_id} ({r.status})</summary>
                                    <pre className="mt-2 text-xs bg-gray-900 text-green-400 p-2 rounded overflow-x-auto whitespace-pre-wrap">
                                        {r.log_content}
                                    </pre>
                                </details>
                            ))
                        )}
                    </div>
                )}


                {activeTab === 'tools' && (
                    <div className="max-w-2xl space-y-6">
                        <form onSubmit={handleSave} className="bg-white p-6 rounded-lg shadow border space-y-4">
                            <h3 className="font-bold text-lg mb-4">Tools & Permissions</h3>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-2">Agent Mode</label>
                                <select value={formData.mode} onChange={e => setFormData({...formData, mode: e.target.value})} className="w-full border rounded p-2">
                                    <option value="primary">Primary</option>
                                    <option value="subagent">Subagent</option>
                                </select>
                            </div>
                            <div className="pt-4">
                                <label className="block text-sm font-medium text-gray-700 mb-2">Available Tools</label>
                                <div className="space-y-2">
                                    {['bash', 'read', 'edit', 'glob', 'grep', 'webfetch', 'task', 'todowrite', 'websearch', 'lsp', 'skill', 'update_task_status'].map(tool => {
                                        const perms = JSON.parse(formData.permissions || '{}');
                                        const isEnabled = tool === 'update_task_status' ? true : (perms[tool] !== 'deny');
                                        return (
                                            <div key={tool} className="flex items-center">
                                                <input
                                                    type="checkbox"
                                                    id={`tool-${tool}`}
                                                    checked={isEnabled}
                                                    disabled={tool === 'update_task_status'}
                                                    onChange={(e) => {
                                                        const newPerms = { ...perms };
                                                        if (e.target.checked) {
                                                            newPerms[tool] = 'allow';
                                                        } else {
                                                            newPerms[tool] = 'deny';
                                                        }
                                                        setFormData({ ...formData, permissions: JSON.stringify(newPerms) });
                                                    }}
                                                    className="h-4 w-4 text-indigo-600 focus:ring-indigo-500 border-gray-300 rounded disabled:opacity-50"
                                                />
                                                <label htmlFor={`tool-${tool}`} className="ml-2 block text-sm text-gray-900">
                                                    {tool}
                                                    {tool === 'update_task_status' && <span className="ml-2 text-xs text-gray-400">(always enabled — part of Paperclip2 MCP)</span>}
                                                </label>
                                            </div>
                                        );
                                    })}
                                </div>
                            </div>
                            <div className="pt-4 border-t mt-6">
                                <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700">
                                    <Save size={16} className="mr-2" /> Save Changes
                                </button>
                            </div>
                        </form>

                        <div className="bg-white p-6 rounded-lg shadow border space-y-4">
                            <div className="flex items-center justify-between mb-2">
                                <h3 className="font-bold text-lg">Tools & MCPs</h3>
                                <Link to={`/companies/${shortName}/mcp-servers`} className="text-xs text-indigo-600 hover:text-indigo-800">Manage Tools & MCPs</Link>
                            </div>

                            {/* Paperclip2 — always on */}
                            {(() => {
                                const p2 = mcpServers.find((s: any) => s.name === 'paperclip2');
                                if (!p2) return null;
                                return (
                                    <div className="flex items-center gap-3 p-3 bg-indigo-50 rounded-lg border border-indigo-100">
                                        <input type="checkbox" checked disabled className="h-4 w-4 text-indigo-600 border-gray-300 rounded opacity-60" />
                                        <div className="flex-1 min-w-0">
                                            <span className="text-sm font-medium text-gray-900">{p2.display_name}</span>
                                            <span className="ml-2 text-xs text-indigo-600 font-medium">Always On</span>
                                            <p className="text-xs text-gray-500 mt-0.5">{p2.description}</p>
                                        </div>
                                    </div>
                                );
                            })()}

                            {/* Optional connected MCPs */}
                            {(() => {
                                const optional = mcpServers.filter((s: any) => s.name !== 'paperclip2' && s.enabled);
                                if (optional.length === 0) return (
                                    <p className="text-sm text-gray-500 italic">No additional MCP servers connected. <Link to={`/companies/${shortName}/mcp-servers`} className="text-indigo-600 hover:underline">Connect one</Link>.</p>
                                );
                                return (
                                    <div className="space-y-2">
                                        {optional.map((srv: any) => (
                                            <div key={srv.id} className="flex items-center gap-3">
                                                <input
                                                    type="checkbox"
                                                    id={`mcp-${srv.id}`}
                                                    checked={!!mcpAssignments[srv.id]}
                                                    onChange={e => setMcpAssignments(prev => ({ ...prev, [srv.id]: e.target.checked }))}
                                                    className="h-4 w-4 text-indigo-600 border-gray-300 rounded"
                                                />
                                                <label htmlFor={`mcp-${srv.id}`} className="text-sm text-gray-900 flex-1">
                                                    <span className="font-medium">{srv.display_name || srv.name}</span>
                                                    {srv.description && <span className="ml-2 text-xs text-gray-400">{srv.description}</span>}
                                                </label>
                                            </div>
                                        ))}
                                    </div>
                                );
                            })()}

                            <div className="pt-3 border-t">
                                <button onClick={handleSaveMCPAssignments} className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700 text-sm">
                                    <Save size={14} className="mr-2" /> Save
                                </button>
                            </div>
                        </div>
                    </div>
                )}

                {activeTab === 'settings' && (
                    <form onSubmit={handleSave} className="bg-white p-6 rounded-lg shadow border max-w-2xl space-y-4">
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Name</label>
                            <input type="text" required value={formData.name} onChange={e => setFormData({...formData, name: e.target.value})} className="w-full border rounded p-2" />
                        </div>
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
                            <input type="text" value={formData.description} onChange={e => setFormData({...formData, description: e.target.value})} className="w-full border rounded p-2" />
                        </div>
                        <div>
                            <div className="flex justify-between items-center mb-1">
                                <label className="block text-sm font-medium text-gray-700">LLM Provider</label>
                                <Link to={`/companies/${shortName}/providers`} className="text-xs text-indigo-600 hover:text-indigo-800">Manage Providers</Link>
                            </div>
                            <select value={formData.provider_id || ''} onChange={e => {
                                const selectedProviderId = e.target.value;
                                const provider = providers.find(p => p.id.toString() === selectedProviderId);
                                setFormData({...formData, provider_id: selectedProviderId, model: provider?.default_model || ''});
                            }} className="w-full border rounded p-2">
                                <option value="">-- Select Provider --</option>
                                {providers.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                            </select>
                        </div>
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Model Name</label>
                            <select required value={formData.model || ''} onChange={e => setFormData({...formData, model: e.target.value})} className="w-full border rounded p-2">
                                <option value="">-- Select Model --</option>
                                {providers.find(p => p.id.toString() === formData.provider_id)?.supported_models?.split(',').map((m: string) => m.trim()).filter((m: string) => m).map((m: string) => (
                                    <option key={m} value={m}>{m}</option>
                                ))}
                            </select>
                        </div>
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">System Prompt</label>
                            <textarea required rows={5} value={formData.system_prompt} onChange={e => setFormData({...formData, system_prompt: e.target.value})} className="w-full border rounded p-2 font-mono text-sm" />
                        </div>
                        <div className="pt-4">
                            <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700">
                                <Save size={16} className="mr-2" /> Save Changes
                            </button>
                        </div>
                    </form>
                )}
            </div>
        </div>
    );
};
