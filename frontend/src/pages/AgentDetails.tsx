import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';

import { ArrowLeft, Save, ChevronDown, ChevronRight } from 'lucide-react';
import { ProviderOrGroupSelect, describeGroupModels } from '../components/ProviderOrGroupSelect';

export const AgentDetails: React.FC = () => {
    const { id, shortName } = useParams<{id: string, shortName: string}>();
    const [agent, setAgent] = useState<any>(null);
    const [stats, setStats] = useState<any>(null);
    const [toolNames, setToolNames] = useState<string[]>([]);
    const [providers, setProviders] = useState<any[]>([]);
    const [activeTab, setActiveTab] = useState('overview');
    const [runs, setRuns] = useState<any[]>([]);
    const [mcpServers, setMcpServers] = useState<any[]>([]);
    const [mcpAccountAssignments, setMcpAccountAssignments] = useState<Record<number, boolean>>({});
    const [codegraphAssignments, setCodegraphAssignments] = useState<Record<number, boolean>>({}); // serverID → enabled (default true)
    // toolFilters: serverID → toolName → enabled (undefined = enabled by default)
    const [toolFilters, setToolFilters] = useState<Record<number, Record<string, boolean>>>({});
    const [expandedToolServers, setExpandedToolServers] = useState<Record<number, boolean>>({});
    // key: `${serverId}:${toolName}` → whether description is expanded
    const [expandedToolDesc, setExpandedToolDesc] = useState<Record<string, boolean>>({});
    const [saveState, setSaveState] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');
    const [saveError, setSaveError] = useState<string | null>(null);
    const [mcpSaveState, setMcpSaveState] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');
    const [mcpSaveError, setMcpSaveError] = useState<string | null>(null);

    const [modelGroups, setModelGroups] = useState<any[]>([]);
    const [formData, setFormData] = useState({ name: '', description: '', system_prompt: '', model: '', provider_id: '', model_group_id: '', mode: 'primary', permissions: '{}' });

    const fetchData = useCallback(async () => {
        try {
            const [agentRes, statsRes, provRes, groupRes, toolNamesRes] = await Promise.all([
                axios.get(`/api/agents/${id}`),
                axios.get(`/api/agents/${id}/stats`),
                axios.get('/api/providers'),
                axios.get('/api/model-groups'),
                axios.get('/api/tool-names')
            ]);
            setAgent(agentRes.data);
            setStats(statsRes.data);
            setProviders(provRes.data || []);
            setModelGroups(groupRes.data || []);
            setToolNames(toolNamesRes.data || []);
            setFormData({
                name: agentRes.data.name,
                description: agentRes.data.description || '',
                system_prompt: agentRes.data.system_prompt,
                model: agentRes.data.model || '',
                provider_id: agentRes.data.provider_id?.toString() || '',
                model_group_id: agentRes.data.model_group_id?.toString() || '',
                mode: agentRes.data.mode || 'primary', permissions: agentRes.data.permissions || '{}'
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
        if (!id || !agent?.company_id) return;
        try {
            const [serversRes, assignRes, toolFilterRes] = await Promise.all([
                axios.get(`/api/mcp-servers?company_id=${agent.company_id}`),
                axios.get(`/api/agents/${id}/mcp-accounts`),
                axios.get(`/api/agents/${id}/mcp-tool-filters`),
            ]);
            setMcpServers(serversRes.data || []);
            const data = assignRes.data || {};
            const accountMap: Record<number, boolean> = {};
            for (const a of (data.accounts || [])) {
                accountMap[a.mcp_account_id] = a.enabled;
            }
            setMcpAccountAssignments(accountMap);
            const cgMap: Record<number, boolean> = {};
            for (const cg of (data.codegraph || [])) {
                cgMap[cg.server_id] = cg.enabled;
            }
            setCodegraphAssignments(cgMap);
            // toolFilterRes.data is map[serverID]map[toolName]enabled (numbers as string keys from JSON)
            const rawFilters = toolFilterRes.data || {};
            const parsedFilters: Record<number, Record<string, boolean>> = {};
            for (const [serverIdStr, toolMap] of Object.entries(rawFilters)) {
                parsedFilters[parseInt(serverIdStr)] = toolMap as Record<string, boolean>;
            }
            setToolFilters(parsedFilters);
        } catch (e) {
            console.error(e);
        }
    }, [id, agent?.company_id]);

    useEffect(() => {
        fetchData();
    }, [fetchData]);

    useEffect(() => {
        if (activeTab === 'tools') fetchMCPData();
    }, [activeTab, fetchMCPData]);

    const handleSaveMCPAssignments = async () => {
        setMcpSaveState('saving');
        setMcpSaveError(null);
        try {
            const accounts = Object.entries(mcpAccountAssignments).map(([accountId, enabled]) => ({
                mcp_account_id: parseInt(accountId),
                enabled,
            }));
            const codegraph = Object.entries(codegraphAssignments).map(([serverId, enabled]) => ({
                server_id: parseInt(serverId),
                enabled,
            }));
            // Build tool filter payload: array of {mcp_server_id, tool_name, enabled}
            const toolFilterPayload: {mcp_server_id: number; tool_name: string; enabled: boolean}[] = [];
            for (const [serverIdStr, toolMap] of Object.entries(toolFilters)) {
                const serverId = parseInt(serverIdStr);
                for (const [toolName, enabled] of Object.entries(toolMap)) {
                    toolFilterPayload.push({ mcp_server_id: serverId, tool_name: toolName, enabled });
                }
            }
            await Promise.all([
                axios.put(`/api/agents/${id}/mcp-accounts`, { accounts, codegraph }),
                axios.put(`/api/agents/${id}/mcp-tool-filters`, toolFilterPayload),
            ]);
            setMcpSaveState('saved');
            setTimeout(() => setMcpSaveState('idle'), 2000);
        } catch (e: any) {
            const msg = e.response?.data?.error || e.message || 'Save failed';
            setMcpSaveError(msg);
            setMcpSaveState('error');
        }
    };

    const handleSave = async (e: React.FormEvent) => {
        e.preventDefault();
        setSaveState('saving');
        setSaveError(null);
        try {
            const payload = {
                ...formData,
                provider_id: formData.model_group_id ? null : (formData.provider_id ? parseInt(formData.provider_id) : null),
                model_group_id: formData.model_group_id ? parseInt(formData.model_group_id) : null
            };
            await axios.put(`/api/agents/${id}`, payload);
            fetchData();
            setSaveState('saved');
            setTimeout(() => setSaveState('idle'), 2000);
        } catch (e: any) {
            setSaveError(e.response?.data?.error || e.message || 'Save failed');
            setSaveState('error');
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
                    {[
                        { key: 'overview', label: 'Overview' },
                        { key: 'logs', label: 'Logs' },
                        { key: 'settings', label: 'Settings' },
                        { key: 'tools', label: 'Tools & MCPs' },
                    ].map(({ key, label }) => (
                        <button
                            key={key}
                            onClick={() => setActiveTab(key)}
                            className={`${activeTab === key ? 'border-indigo-500 text-indigo-600' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'} whitespace-nowrap py-4 px-1 border-b-2 font-medium text-sm`}
                        >
                            {label}
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
                                    <p className="text-sm text-gray-500 mb-1">{agent.model_group_id ? 'Model Group' : 'Active Provider'}</p>
                                    <p className="font-medium">
                                        {agent.model_group_id
                                            ? <>{modelGroups.find(g => g.id === agent.model_group_id)?.name || `Group #${agent.model_group_id}`} <span className="ml-1 text-xs font-normal bg-indigo-100 text-indigo-700 px-2 py-0.5 rounded-full">auto-routing</span></>
                                            : (providers.find(p => p.id === agent.provider_id)?.name || 'None')}
                                    </p>
                                </div>
                                <div>
                                    <p className="text-sm text-gray-500 mb-1">{agent.model_group_id ? 'Models' : 'Active Model'}</p>
                                    <p className="font-medium">
                                        {agent.model_group_id
                                            ? describeGroupModels(modelGroups.find(g => g.id === agent.model_group_id))
                                            : (agent.model || 'None')}
                                    </p>
                                </div>
                                <div className="pt-2">
                                    <p className="text-sm text-gray-500 mb-2">Proxy URL for CLI</p>
                                    <p className="font-mono text-sm bg-gray-100 p-2 rounded break-all">
                                        {window.location.protocol}//{window.location.host}/api/proxy/agent/{agent.id}/v1/chat/completions
                                    </p>
                                    <p className="mt-1 text-xs text-gray-400">
                                        Requests must carry your login session (cookie) or an agent run token — anonymous calls are rejected.
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
                                    {toolNames.map(tool => {
                                        const perms = JSON.parse(formData.permissions || '{}');
                                        const isEnabled = perms[tool] !== 'deny';
                                        return (
                                            <div key={tool} className="flex items-center">
                                                <input
                                                    type="checkbox"
                                                    id={`tool-${tool}`}
                                                    checked={isEnabled}
                                                    onChange={(e) => {
                                                        const newPerms = { ...perms };
                                                        if (e.target.checked) {
                                                            newPerms[tool] = 'allow';
                                                        } else {
                                                            newPerms[tool] = 'deny';
                                                        }
                                                        setFormData({ ...formData, permissions: JSON.stringify(newPerms) });
                                                    }}
                                                    className="h-4 w-4 text-indigo-600 focus:ring-indigo-500 border-gray-300 rounded"
                                                />
                                                <label htmlFor={`tool-${tool}`} className="ml-2 block text-sm text-gray-900">
                                                    {tool}
                                                </label>
                                            </div>
                                        );
                                    })}
                                </div>
                            </div>
                            <div className="pt-4 border-t mt-6 flex items-center gap-3">
                                <button type="submit" disabled={saveState === 'saving'} className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700 disabled:opacity-60">
                                    <Save size={16} className="mr-2" /> {saveState === 'saving' ? 'Saving…' : 'Save Changes'}
                                </button>
                                {saveState === 'saved' && <span className="text-sm text-green-600">Saved</span>}
                                {saveState === 'error' && <span className="text-sm text-red-600">{saveError}</span>}
                            </div>
                        </form>

                        <div className="bg-white p-6 rounded-lg shadow border space-y-4">
                            <div className="flex items-center justify-between mb-2">
                                <h3 className="font-bold text-lg">Tools & MCPs</h3>
                                <Link to={`/companies/${shortName}/mcp-servers`} className="text-xs text-indigo-600 hover:text-indigo-800">Manage Tools & MCPs</Link>
                            </div>

                            {/* Headcount1 — always on */}
                            {(() => {
                                const p2 = mcpServers.find((s: any) => s.name === 'headcount1');
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

                            {/* Optional connected MCPs — grouped by server, per-account checkboxes + per-tool filters */}
                            {(() => {
                                const optional = mcpServers.filter((s: any) => s.name !== 'headcount1' && !s.builtin && !s.project_id);
                                const builtin = mcpServers.filter((s: any) => s.name !== 'headcount1' && s.builtin && !s.project_id);
                                const allExternal = [...builtin, ...optional];
                                if (allExternal.length === 0) return (
                                    <p className="text-sm text-gray-500 italic">No additional MCP servers connected. <Link to={`/companies/${shortName}/mcp-servers`} className="text-indigo-600 hover:underline">Connect one</Link>.</p>
                                );
                                return (
                                    <div className="space-y-3">
                                        {allExternal.map((srv: any) => {
                                            const accounts: any[] = srv.accounts || [];
                                            const hasEnabledAccount = accounts.some((acc: any) => !!mcpAccountAssignments[acc.id]);
                                            // Parse cached tools for this server
                                            let cachedTools: {name: string; description?: string}[] = [];
                                            try { if (srv.tools_cache) cachedTools = JSON.parse(srv.tools_cache); } catch {}
                                            const serverToolFilters = toolFilters[srv.id] || {};
                                            const toolsExpanded = !!expandedToolServers[srv.id];
                                            return (
                                                <div key={srv.id} className="border border-gray-100 rounded-lg overflow-hidden">
                                                    <div className="flex items-center justify-between px-3 py-2 bg-gray-50 border-b border-gray-100">
                                                        <span className="text-sm font-semibold text-gray-800">{srv.display_name || srv.name}</span>
                                                        {accounts.length === 0 && (
                                                            <Link to={`/companies/${shortName}/mcp-servers`} className="text-xs text-amber-600 hover:text-amber-800">Not connected</Link>
                                                        )}
                                                    </div>
                                                    {accounts.length === 0 ? (
                                                        <p className="px-3 py-2 text-xs text-gray-400 italic">No accounts configured. <Link to={`/companies/${shortName}/mcp-servers`} className="text-indigo-500 hover:underline">Authorize</Link>.</p>
                                                    ) : (
                                                        <>
                                                            <div className="divide-y divide-gray-50">
                                                                {accounts.map((acc: any) => (
                                                                    <label key={acc.id} className="flex items-center gap-3 px-3 py-2 hover:bg-gray-50 cursor-pointer">
                                                                        <input
                                                                            type="checkbox"
                                                                            checked={!!mcpAccountAssignments[acc.id]}
                                                                            onChange={e => setMcpAccountAssignments(prev => ({ ...prev, [acc.id]: e.target.checked }))}
                                                                            className="h-4 w-4 text-indigo-600 border-gray-300 rounded flex-shrink-0"
                                                                        />
                                                                        <div className="min-w-0">
                                                                            <span className="text-sm text-gray-900 font-medium">{acc.name}</span>
                                                                            {!acc.has_token && <span className="ml-2 text-xs text-amber-500">No token</span>}
                                                                        </div>
                                                                    </label>
                                                                ))}
                                                            </div>
                                                            {hasEnabledAccount && cachedTools.length > 0 && (
                                                                <div className="border-t border-gray-100">
                                                                    <button
                                                                        type="button"
                                                                        onClick={() => setExpandedToolServers(prev => ({ ...prev, [srv.id]: !prev[srv.id] }))}
                                                                        className="w-full flex items-center gap-1.5 px-3 py-1.5 text-xs text-gray-500 hover:text-gray-700 hover:bg-gray-50"
                                                                    >
                                                                        {toolsExpanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                                                                        <span>
                                                                            {cachedTools.length} tool{cachedTools.length !== 1 ? 's' : ''}
                                                                            {Object.values(serverToolFilters).some(v => !v) && (
                                                                                <span className="ml-1 text-amber-600">
                                                                                    ({Object.values(serverToolFilters).filter(v => !v).length} disabled)
                                                                                </span>
                                                                            )}
                                                                        </span>
                                                                    </button>
                                                                    {toolsExpanded && (
                                                                        <div className="px-3 pb-2 space-y-1">
                                                                            <div className="flex gap-3 pb-1 border-b border-gray-100 mb-1">
                                                                                <button
                                                                                    type="button"
                                                                                    onClick={() => setToolFilters(prev => ({
                                                                                        ...prev,
                                                                                        [srv.id]: Object.fromEntries(cachedTools.map((t: any) => [t.name, true])),
                                                                                    }))}
                                                                                    className="text-xs text-indigo-600 hover:text-indigo-800"
                                                                                >Select all</button>
                                                                                <button
                                                                                    type="button"
                                                                                    onClick={() => setToolFilters(prev => ({
                                                                                        ...prev,
                                                                                        [srv.id]: Object.fromEntries(cachedTools.map((t: any) => [t.name, false])),
                                                                                    }))}
                                                                                    className="text-xs text-gray-500 hover:text-gray-700"
                                                                                >Unselect all</button>
                                                                            </div>
                                                                            {cachedTools.map((tool: any) => {
                                                                                const isEnabled = serverToolFilters[tool.name] !== false;
                                                                                const descKey = `${srv.id}:${tool.name}`;
                                                                                const descExpanded = !!expandedToolDesc[descKey];
                                                                                return (
                                                                                    <div key={tool.name} className="flex items-start gap-2 py-0.5">
                                                                                        <input
                                                                                            type="checkbox"
                                                                                            checked={isEnabled}
                                                                                            onChange={e => {
                                                                                                setToolFilters(prev => ({
                                                                                                    ...prev,
                                                                                                    [srv.id]: {
                                                                                                        ...(prev[srv.id] || {}),
                                                                                                        [tool.name]: e.target.checked,
                                                                                                    },
                                                                                                }));
                                                                                            }}
                                                                                            className="h-3.5 w-3.5 text-indigo-600 border-gray-300 rounded flex-shrink-0 mt-0.5 cursor-pointer"
                                                                                        />
                                                                                        <div
                                                                                            className="min-w-0 flex-1 cursor-pointer"
                                                                                            onClick={() => tool.description && setExpandedToolDesc(prev => ({ ...prev, [descKey]: !prev[descKey] }))}
                                                                                        >
                                                                                            <span className="text-xs font-mono text-gray-800">{tool.name}</span>
                                                                                            {tool.description && (
                                                                                                <p className={`text-xs text-gray-400 ${descExpanded ? '' : 'truncate'}`}>{tool.description}</p>
                                                                                            )}
                                                                                        </div>
                                                                                    </div>
                                                                                );
                                                                            })}
                                                                        </div>
                                                                    )}
                                                                </div>
                                                            )}
                                                        </>
                                                    )}
                                                </div>
                                            );
                                        })}
                                    </div>
                                );
                            })()}

                            {/* CodeGraph servers — checkboxes to enable/disable per agent */}
                            {(() => {
                                const codegraph = mcpServers.filter((s: any) => !!s.project_id);
                                if (codegraph.length === 0) return null;
                                return (
                                    <div>
                                        <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">CodeGraph (auto-attached per project)</p>
                                        <div className="space-y-2">
                                            {codegraph.map((srv: any) => {
                                                const ready = srv.init_status === 'ready';
                                                const initializing = srv.init_status === 'initializing';
                                                const enabled = srv.id in codegraphAssignments ? codegraphAssignments[srv.id] : true;
                                                return (
                                                    <label key={srv.id} className={`border rounded-lg px-3 py-2 flex items-center gap-3 cursor-pointer ${ready ? 'border-green-100 bg-green-50' : 'border-gray-100 bg-gray-50'}`}>
                                                        <input
                                                            type="checkbox"
                                                            checked={enabled}
                                                            onChange={e => setCodegraphAssignments(prev => ({ ...prev, [srv.id]: e.target.checked }))}
                                                            className="h-4 w-4 text-indigo-600 border-gray-300 rounded flex-shrink-0"
                                                        />
                                                        <span className="text-sm font-medium text-gray-800 flex-1">{srv.display_name || srv.name}</span>
                                                        {ready ? (
                                                            <span className="text-xs text-green-700 font-medium">Ready</span>
                                                        ) : initializing ? (
                                                            <span className="text-xs text-amber-600 font-medium">Initializing…</span>
                                                        ) : srv.init_status?.startsWith('error:') ? (
                                                            <span className="text-xs text-red-500 font-medium">Init failed</span>
                                                        ) : (
                                                            <span className="text-xs text-gray-400">Not initialized</span>
                                                        )}
                                                    </label>
                                                );
                                            })}
                                        </div>
                                        <p className="text-xs text-gray-400 mt-1.5">When enabled, codegraph tools are attached to runs for tasks that belong to that project — not for every task. Uncheck to disable for this agent.</p>
                                    </div>
                                );
                            })()}

                            <div className="pt-3 border-t flex items-center gap-3">
                                <button
                                    onClick={handleSaveMCPAssignments}
                                    disabled={mcpSaveState === 'saving'}
                                    className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700 text-sm disabled:opacity-60"
                                >
                                    <Save size={14} className="mr-2" />
                                    {mcpSaveState === 'saving' ? 'Saving…' : 'Save'}
                                </button>
                                {mcpSaveState === 'saved' && <span className="text-sm text-green-600">Saved</span>}
                                {mcpSaveState === 'error' && <span className="text-sm text-red-600">{mcpSaveError}</span>}
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
                        <ProviderOrGroupSelect
                            label="LLM Provider or Model Group"
                            providers={providers}
                            modelGroups={modelGroups}
                            manageLinkTo={`/companies/${shortName}/providers`}
                            modelRequired
                            value={{ provider_id: formData.provider_id, model_group_id: formData.model_group_id, model: formData.model }}
                            onChange={v => setFormData({ ...formData, provider_id: v.provider_id, model_group_id: v.model_group_id, model: v.model })}
                        />
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">System Prompt</label>
                            <textarea required rows={5} value={formData.system_prompt} onChange={e => setFormData({...formData, system_prompt: e.target.value})} className="w-full border rounded p-2 font-mono text-sm" />
                        </div>
                        <div className="pt-4 flex items-center gap-3">
                            <button type="submit" disabled={saveState === 'saving'} className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700 disabled:opacity-60">
                                <Save size={16} className="mr-2" /> {saveState === 'saving' ? 'Saving…' : 'Save Changes'}
                            </button>
                            {saveState === 'saved' && <span className="text-sm text-green-600">Saved</span>}
                            {saveState === 'error' && <span className="text-sm text-red-600">{saveError}</span>}
                        </div>
                    </form>
                )}
            </div>
        </div>
    );
};
