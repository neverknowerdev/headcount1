import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { Plus, Trash2, Edit2, Search, Power, Shield, Terminal, Globe, Cpu, Key, CheckCircle2, AlertCircle, GitBranch, FileText } from 'lucide-react';

interface MCPServer {
    id: number;
    name: string;
    display_name: string;
    description: string;
    transport: 'stdio' | 'http' | 'builtin';
    command: string;
    args: string;
    url: string;
    headers: string;
    auth_type: string;
    auth_env_var: string;
    auth_token: string;
    enabled: boolean;
    builtin: boolean;
    created_at: string;
    updated_at: string;
}

interface MCPTool {
    name: string;
    description: string;
    inputSchema?: any;
}

const PAPERCLIP2_TOOLS: MCPTool[] = [
    { name: 'update_task_status', description: 'Update the status of the current task (to-do, in-progress, in-review, done, blocked, cancelled).' },
    { name: 'create_subtask', description: 'Create a new subtask and assign it to a sub-agent for execution.' },
];

const transportIcon = (t: string) => {
    if (t === 'stdio') return <Terminal size={14} className="inline mr-1" />;
    if (t === 'http') return <Globe size={14} className="inline mr-1" />;
    return <Cpu size={14} className="inline mr-1" />;
};

const serverIcon = (name: string) => {
    if (name === 'github') return <GitBranch size={20} />;
    if (name === 'google-docs') return <FileText size={20} />;
    if (name === 'paperclip2') return <Cpu size={20} />;
    return <Cpu size={20} />;
};

const authLabel = (authType: string) => {
    if (authType === 'bearer') return 'Personal Access Token';
    if (authType === 'credentials-file') return 'Credentials file path';
    return 'Token';
};

const emptyForm = {
    name: '', display_name: '', description: '',
    transport: 'stdio' as MCPServer['transport'],
    command: '', args: '[]', url: '', headers: '{}',
    auth_type: 'none', auth_token: '', enabled: true,
};

export const MCPServers: React.FC = () => {
    const [servers, setServers] = useState<MCPServer[]>([]);
    const [isModalOpen, setIsModalOpen] = useState(false);
    const [editingId, setEditingId] = useState<number | null>(null);
    const [editingBuiltin, setEditingBuiltin] = useState(false);
    const [formData, setFormData] = useState<typeof emptyForm>({ ...emptyForm });
    const [discoveredTools, setDiscoveredTools] = useState<Record<number, MCPTool[]>>({});
    const [discovering, setDiscovering] = useState<number | null>(null);
    const [discoverErrors, setDiscoverErrors] = useState<Record<number, string>>({});
    const [error, setError] = useState<string | null>(null);

    const fetchServers = useCallback(async () => {
        try {
            const res = await axios.get('/api/mcp-servers');
            setServers(res.data || []);
        } catch (e) {
            console.error(e);
        }
    }, []);

    // Auto-show paperclip2 tools on load
    useEffect(() => {
        fetchServers();
    }, [fetchServers]);

    useEffect(() => {
        const p2 = servers.find(s => s.name === 'paperclip2');
        if (p2 && !discoveredTools[p2.id]) {
            setDiscoveredTools(prev => ({ ...prev, [p2.id]: PAPERCLIP2_TOOLS }));
        }
    }, [servers]);

    const openModal = (s?: MCPServer) => {
        setError(null);
        if (s) {
            setEditingId(s.id);
            setEditingBuiltin(s.builtin);
            setFormData({
                name: s.name,
                display_name: s.display_name || '',
                description: s.description || '',
                transport: (s.transport === 'builtin' ? 'stdio' : s.transport) as MCPServer['transport'],
                command: s.command || '',
                args: s.args || '[]',
                url: s.url || '',
                headers: s.headers || '{}',
                auth_type: s.auth_type || 'none',
                auth_token: '',
                enabled: s.enabled,
            });
        } else {
            setEditingId(null);
            setEditingBuiltin(false);
            setFormData({ ...emptyForm });
        }
        setIsModalOpen(true);
    };

    const openConnectModal = (s: MCPServer) => {
        setError(null);
        setEditingId(s.id);
        setEditingBuiltin(true);
        setFormData({
            name: s.name,
            display_name: s.display_name || '',
            description: s.description || '',
            transport: s.transport as MCPServer['transport'],
            command: s.command || '',
            args: s.args || '[]',
            url: s.url || '',
            headers: s.headers || '{}',
            auth_type: s.auth_type || 'none',
            auth_token: '',
            enabled: true,
        });
        setIsModalOpen(true);
    };

    const handleSave = async (e: React.FormEvent) => {
        e.preventDefault();
        setError(null);
        try {
            const payload = { ...formData };
            if (editingId) {
                await axios.put(`/api/mcp-servers/${editingId}`, payload);
            } else {
                await axios.post('/api/mcp-servers', payload);
            }
            setIsModalOpen(false);
            fetchServers();
        } catch (e: any) {
            setError(e.response?.data?.error || 'Save failed');
        }
    };

    const handleDelete = async (id: number) => {
        if (!window.confirm('Delete this MCP server?')) return;
        try {
            await axios.delete(`/api/mcp-servers/${id}`);
            fetchServers();
        } catch (e: any) {
            alert(e.response?.data?.error || 'Delete failed');
        }
    };

    const handleToggleEnabled = async (s: MCPServer) => {
        try {
            await axios.put(`/api/mcp-servers/${s.id}`, { ...s, auth_token: '', enabled: !s.enabled });
            fetchServers();
        } catch (e) {
            console.error(e);
        }
    };

    const handleDiscover = async (id: number) => {
        setDiscovering(id);
        setDiscoverErrors(prev => { const n = { ...prev }; delete n[id]; return n; });
        try {
            const res = await axios.post(`/api/mcp-servers/${id}/discover`);
            setDiscoveredTools(prev => ({ ...prev, [id]: res.data.tools || [] }));
        } catch (e: any) {
            const msg = e.response?.data?.error || e.message || 'Unknown error';
            setDiscoverErrors(prev => ({ ...prev, [id]: msg }));
        } finally {
            setDiscovering(null);
        }
    };

    // Split servers: paperclip2 first, then other predefined, then custom
    const paperclip2 = servers.find(s => s.name === 'paperclip2');
    const predefined = servers.filter(s => s.builtin && s.name !== 'paperclip2');
    const custom = servers.filter(s => !s.builtin);

    return (
        <div className="h-full flex flex-col space-y-6">
            <div className="flex justify-between items-center">
                <div>
                    <h1 className="text-2xl font-bold">Tools & MCPs</h1>
                    <p className="text-sm text-gray-500 mt-1">Connect MCP servers to extend agent capabilities.</p>
                </div>
                <button
                    onClick={() => openModal()}
                    className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700 shadow-sm"
                >
                    <Plus size={16} className="mr-2" /> Add MCP Server
                </button>
            </div>

            {/* Built-in paperclip2 — always on */}
            {paperclip2 && (
                <section>
                    <h2 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">Built-in</h2>
                    <div className="bg-white rounded-lg border border-indigo-100 shadow-sm p-5">
                        <div className="flex items-start gap-3">
                            <div className="p-2 bg-indigo-50 rounded-lg text-indigo-600 flex-shrink-0">
                                {serverIcon(paperclip2.name)}
                            </div>
                            <div className="flex-1 min-w-0">
                                <div className="flex items-center gap-2 mb-1">
                                    <h3 className="text-base font-semibold text-gray-900">{paperclip2.display_name}</h3>
                                    <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-indigo-100 text-indigo-700">
                                        <Shield size={10} className="mr-1" /> Always On
                                    </span>
                                </div>
                                <p className="text-sm text-gray-600 mb-3">{paperclip2.description}</p>
                                <div className="grid grid-cols-1 sm:grid-cols-2 gap-1.5">
                                    {PAPERCLIP2_TOOLS.map(tool => (
                                        <div key={tool.name} className="bg-indigo-50 rounded p-2">
                                            <p className="text-xs font-mono font-semibold text-indigo-800">{tool.name}</p>
                                            <p className="text-xs text-gray-500 mt-0.5">{tool.description}</p>
                                        </div>
                                    ))}
                                </div>
                            </div>
                        </div>
                    </div>
                </section>
            )}

            {/* Pre-defined integrations */}
            {predefined.length > 0 && (
                <section>
                    <h2 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">Pre-configured Integrations</h2>
                    <div className="grid grid-cols-1 gap-4">
                        {predefined.map(s => (
                            <PredefinedServerCard
                                key={s.id}
                                server={s}
                                tools={discoveredTools[s.id]}
                                discovering={discovering === s.id}
                                discoverError={discoverErrors[s.id]}
                                onConnect={() => openConnectModal(s)}
                                onToggle={() => handleToggleEnabled(s)}
                                onDiscover={() => handleDiscover(s.id)}
                            />
                        ))}
                    </div>
                </section>
            )}

            {/* Custom servers */}
            {custom.length > 0 && (
                <section>
                    <h2 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">Custom Servers</h2>
                    <div className="grid grid-cols-1 gap-4">
                        {custom.map(s => (
                            <div key={s.id} className={`bg-white rounded-lg border shadow-sm p-5 ${!s.enabled ? 'opacity-60' : ''}`}>
                                <div className="flex items-start justify-between">
                                    <div className="flex-1 min-w-0">
                                        <div className="flex items-center gap-2 mb-1">
                                            <h3 className="text-base font-semibold text-gray-900">{s.display_name || s.name}</h3>
                                            <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${s.enabled ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-500'}`}>
                                                {s.enabled ? 'Enabled' : 'Disabled'}
                                            </span>
                                            <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-600">
                                                {transportIcon(s.transport)}{s.transport}
                                            </span>
                                        </div>
                                        {s.description && <p className="text-sm text-gray-600 mb-2">{s.description}</p>}
                                        {s.transport === 'stdio' && s.command && (
                                            <p className="text-xs text-gray-400 font-mono truncate">$ {s.command} {s.args && s.args !== '[]' ? JSON.parse(s.args).join(' ') : ''}</p>
                                        )}
                                        {s.transport === 'http' && s.url && (
                                            <p className="text-xs text-gray-400 font-mono truncate">{s.url}</p>
                                        )}
                                    </div>
                                    <div className="flex items-center gap-2 ml-4 flex-shrink-0">
                                        <button
                                            onClick={() => handleDiscover(s.id)}
                                            disabled={discovering === s.id}
                                            className="text-blue-500 hover:text-blue-700 px-2 py-1 text-xs border border-blue-200 rounded hover:bg-blue-50 flex items-center gap-1"
                                        >
                                            <Search size={13} />
                                            {discovering === s.id ? 'Loading…' : 'Discover'}
                                        </button>
                                        <button onClick={() => handleToggleEnabled(s)} className={`p-1.5 rounded ${s.enabled ? 'text-green-600 hover:text-green-800' : 'text-gray-400 hover:text-gray-600'}`} title={s.enabled ? 'Disable' : 'Enable'}>
                                            <Power size={16} />
                                        </button>
                                        <button onClick={() => openModal(s)} className="p-1.5 text-gray-500 hover:text-gray-700 rounded">
                                            <Edit2 size={16} />
                                        </button>
                                        <button onClick={() => handleDelete(s.id)} className="p-1.5 text-red-500 hover:text-red-700 rounded">
                                            <Trash2 size={16} />
                                        </button>
                                    </div>
                                </div>
                                {discoverErrors[s.id] && (
                                    <div className="mt-2 text-xs text-red-600 bg-red-50 rounded p-2">{discoverErrors[s.id]}</div>
                                )}
                                {discoveredTools[s.id] && (
                                    <div className="mt-3 pt-3 border-t">
                                        <p className="text-xs font-semibold text-gray-500 mb-2">{discoveredTools[s.id].length} tool{discoveredTools[s.id].length !== 1 ? 's' : ''} available</p>
                                        <div className="grid grid-cols-1 sm:grid-cols-2 gap-1.5">
                                            {discoveredTools[s.id].map(tool => (
                                                <div key={tool.name} className="bg-gray-50 rounded p-2">
                                                    <p className="text-xs font-mono font-semibold text-gray-800">{tool.name}</p>
                                                    {tool.description && <p className="text-xs text-gray-500 mt-0.5 truncate">{tool.description}</p>}
                                                </div>
                                            ))}
                                        </div>
                                    </div>
                                )}
                            </div>
                        ))}
                    </div>
                </section>
            )}

            {servers.length === 0 && (
                <div className="text-center py-16 text-gray-400">
                    <Cpu size={48} className="mx-auto mb-4 opacity-30" />
                    <p className="text-lg">No MCP servers configured.</p>
                </div>
            )}

            {isModalOpen && (
                <div className="fixed inset-0 bg-black/50 flex justify-center items-center z-50">
                    <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-lg max-h-[90vh] flex flex-col">
                        <h2 className="text-xl font-bold mb-4">
                            {editingBuiltin ? `Connect ${formData.display_name}` : (editingId ? 'Edit MCP Server' : 'Add MCP Server')}
                        </h2>
                        <form onSubmit={handleSave} className="space-y-4 overflow-y-auto flex-1 pr-1">
                            {editingBuiltin ? (
                                /* Simplified form for predefined servers */
                                <>
                                    <p className="text-sm text-gray-600">{formData.description}</p>
                                    {formData.auth_type !== 'none' && (
                                        <div>
                                            <label className="block text-sm font-medium text-gray-700 mb-1">
                                                {authLabel(formData.auth_type)}
                                                {editingId && ' (leave blank to keep existing)'}
                                            </label>
                                            <input
                                                type="password"
                                                value={formData.auth_token}
                                                onChange={e => setFormData({ ...formData, auth_token: e.target.value })}
                                                className="w-full border rounded p-2 text-sm"
                                                placeholder={formData.auth_type === 'credentials-file' ? '/path/to/credentials.json' : ''}
                                            />
                                            {formData.name === 'github' && (
                                                <p className="text-xs text-gray-400 mt-1">
                                                    Generate a token at <span className="font-mono">github.com/settings/tokens</span> with <code>repo</code> scope.
                                                </p>
                                            )}
                                            {formData.name === 'google-docs' && (
                                                <p className="text-xs text-gray-400 mt-1">
                                                    Create a service account in Google Cloud Console and download the credentials JSON.
                                                </p>
                                            )}
                                        </div>
                                    )}
                                    <div className="flex items-center gap-2">
                                        <input type="checkbox" id="mcp-enabled" checked={formData.enabled}
                                            onChange={e => setFormData({ ...formData, enabled: e.target.checked })}
                                            className="h-4 w-4 text-indigo-600 border-gray-300 rounded" />
                                        <label htmlFor="mcp-enabled" className="text-sm text-gray-700">Enabled</label>
                                    </div>
                                </>
                            ) : (
                                /* Full form for custom servers */
                                <>
                                    <div className="grid grid-cols-2 gap-3">
                                        <div>
                                            <label className="block text-sm font-medium text-gray-700 mb-1">Name (slug)</label>
                                            <input required type="text" value={formData.name}
                                                onChange={e => setFormData({ ...formData, name: e.target.value })}
                                                placeholder="my-server" className="w-full border rounded p-2 text-sm" />
                                        </div>
                                        <div>
                                            <label className="block text-sm font-medium text-gray-700 mb-1">Display Name</label>
                                            <input type="text" value={formData.display_name}
                                                onChange={e => setFormData({ ...formData, display_name: e.target.value })}
                                                placeholder="My Server" className="w-full border rounded p-2 text-sm" />
                                        </div>
                                    </div>
                                    <div>
                                        <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
                                        <input type="text" value={formData.description}
                                            onChange={e => setFormData({ ...formData, description: e.target.value })}
                                            className="w-full border rounded p-2 text-sm" />
                                    </div>
                                    <div>
                                        <label className="block text-sm font-medium text-gray-700 mb-1">Transport</label>
                                        <select value={formData.transport}
                                            onChange={e => setFormData({ ...formData, transport: e.target.value as any })}
                                            className="w-full border rounded p-2 text-sm">
                                            <option value="stdio">stdio (local process)</option>
                                            <option value="http">HTTP (remote server)</option>
                                        </select>
                                    </div>

                                    {formData.transport === 'stdio' && (
                                        <>
                                            <div>
                                                <label className="block text-sm font-medium text-gray-700 mb-1">Command</label>
                                                <input required type="text" value={formData.command}
                                                    onChange={e => setFormData({ ...formData, command: e.target.value })}
                                                    placeholder="/usr/local/bin/my-mcp"
                                                    className="w-full border rounded p-2 text-sm font-mono" />
                                            </div>
                                            <div>
                                                <label className="block text-sm font-medium text-gray-700 mb-1">Args (JSON array)</label>
                                                <input type="text" value={formData.args}
                                                    onChange={e => setFormData({ ...formData, args: e.target.value })}
                                                    placeholder='["stdio"]'
                                                    className="w-full border rounded p-2 text-sm font-mono" />
                                            </div>
                                        </>
                                    )}

                                    {formData.transport === 'http' && (
                                        <>
                                            <div>
                                                <label className="block text-sm font-medium text-gray-700 mb-1">Server URL</label>
                                                <input required type="url" value={formData.url}
                                                    onChange={e => setFormData({ ...formData, url: e.target.value })}
                                                    placeholder="https://mcp.example.com"
                                                    className="w-full border rounded p-2 text-sm" />
                                            </div>
                                        </>
                                    )}

                                    <div>
                                        <label className="block text-sm font-medium text-gray-700 mb-1">Auth Type</label>
                                        <select value={formData.auth_type}
                                            onChange={e => setFormData({ ...formData, auth_type: e.target.value })}
                                            className="w-full border rounded p-2 text-sm">
                                            <option value="none">None</option>
                                            <option value="bearer">Bearer Token</option>
                                            <option value="credentials-file">Credentials File</option>
                                        </select>
                                    </div>
                                    {formData.auth_type !== 'none' && (
                                        <div>
                                            <label className="block text-sm font-medium text-gray-700 mb-1">
                                                {authLabel(formData.auth_type)} {editingId && '(leave blank to keep existing)'}
                                            </label>
                                            <input type="password" value={formData.auth_token}
                                                onChange={e => setFormData({ ...formData, auth_token: e.target.value })}
                                                className="w-full border rounded p-2 text-sm" />
                                        </div>
                                    )}

                                    <div className="flex items-center gap-2">
                                        <input type="checkbox" id="mcp-enabled" checked={formData.enabled}
                                            onChange={e => setFormData({ ...formData, enabled: e.target.checked })}
                                            className="h-4 w-4 text-indigo-600 border-gray-300 rounded" />
                                        <label htmlFor="mcp-enabled" className="text-sm text-gray-700">Enabled</label>
                                    </div>
                                </>
                            )}

                            {error && <p className="text-sm text-red-600">{error}</p>}
                        </form>
                        <div className="flex justify-end gap-3 pt-4 border-t mt-4">
                            <button type="button" onClick={() => setIsModalOpen(false)} className="text-gray-500 hover:text-gray-700 px-4 py-2 text-sm">Cancel</button>
                            <button onClick={handleSave} className="bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 text-sm">
                                {editingBuiltin ? 'Save' : (editingId ? 'Save Changes' : 'Add Server')}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};

const INSTALL_INSTRUCTIONS: Record<string, { cmd: string; note: string }> = {
    github: {
        cmd: 'brew install github/tap/github-mcp-server',
        note: 'Then generate a token at github.com/settings/tokens with repo + read:org scope.',
    },
    'google-docs': {
        cmd: 'npm install -g @modelcontextprotocol/server-gdrive',
        note: 'Create a service account in Google Cloud Console and download the credentials JSON file.',
    },
};

// Card for pre-defined integrations (github, google-docs, etc.)
function PredefinedServerCard({ server, tools, discovering, discoverError, onConnect, onToggle, onDiscover }: {
    server: MCPServer;
    tools?: MCPTool[];
    discovering: boolean;
    discoverError?: string;
    onConnect: () => void;
    onToggle: () => void;
    onDiscover: () => void;
}) {
    const [showDetails, setShowDetails] = React.useState(false);
    const isConnected = server.enabled;
    const install = INSTALL_INSTRUCTIONS[server.name];

    return (
        <div className={`bg-white rounded-lg border shadow-sm p-5 ${!isConnected ? 'border-gray-200' : 'border-green-200'}`}>
            <div className="flex items-start gap-3">
                <div className={`p-2 rounded-lg flex-shrink-0 ${isConnected ? 'bg-green-50 text-green-700' : 'bg-gray-100 text-gray-500'}`}>
                    {serverIcon(server.name)}
                </div>
                <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 mb-1">
                        <h3 className="text-base font-semibold text-gray-900">{server.display_name}</h3>
                        <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-500">
                            <Shield size={10} className="mr-1" /> Built-in
                        </span>
                        {isConnected ? (
                            <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-green-100 text-green-700">
                                <CheckCircle2 size={10} /> Connected
                            </span>
                        ) : (
                            <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-amber-100 text-amber-700">
                                <AlertCircle size={10} /> Not connected
                            </span>
                        )}
                    </div>
                    <p className="text-sm text-gray-600 mb-3">{server.description}</p>

                    {!isConnected ? (
                        <div className="space-y-3">
                            {install && (
                                <div className="bg-gray-50 rounded p-3 text-xs space-y-1">
                                    <p className="font-semibold text-gray-600">Setup:</p>
                                    <p className="font-mono text-gray-700 bg-gray-100 rounded px-2 py-1">{install.cmd}</p>
                                    <p className="text-gray-500">{install.note}</p>
                                </div>
                            )}
                            <button
                                onClick={onConnect}
                                className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-indigo-600 text-white text-sm rounded hover:bg-indigo-700"
                            >
                                <Key size={13} /> Connect
                            </button>
                        </div>
                    ) : (
                        <div className="space-y-2">
                            <div className="flex items-center gap-2 flex-wrap">
                                <button
                                    onClick={onDiscover}
                                    disabled={discovering}
                                    className="text-blue-500 hover:text-blue-700 px-2 py-1 text-xs border border-blue-200 rounded hover:bg-blue-50 flex items-center gap-1"
                                >
                                    <Search size={13} />
                                    {discovering ? 'Loading…' : 'Discover Tools'}
                                </button>
                                <button onClick={onConnect} className="text-xs text-gray-600 hover:text-gray-900 px-2 py-1 border border-gray-200 rounded flex items-center gap-1">
                                    <Key size={12} /> Re-authenticate
                                </button>
                                <button onClick={() => setShowDetails(v => !v)} className="text-xs text-gray-500 hover:text-gray-700 px-2 py-1 border border-gray-200 rounded flex items-center gap-1">
                                    <Edit2 size={12} /> {showDetails ? 'Hide details' : 'Details'}
                                </button>
                                <button onClick={onToggle} className="text-xs text-red-500 hover:text-red-700 px-2 py-1 border border-red-200 rounded flex items-center gap-1">
                                    <Power size={12} /> Disconnect
                                </button>
                            </div>
                            {discoverError && (
                                <div className="text-xs text-red-600 bg-red-50 border border-red-100 rounded p-2">
                                    <span className="font-semibold">Discovery failed:</span> {discoverError}
                                    {install && <span className="block mt-1 text-gray-500">Make sure the binary is installed: <span className="font-mono">{install.cmd}</span></span>}
                                </div>
                            )}
                            {showDetails && (
                                <div className="bg-gray-50 rounded p-3 text-xs space-y-1 border border-gray-100">
                                    {server.command && <p><span className="font-semibold text-gray-500">Command:</span> <span className="font-mono text-gray-700">{server.command} {server.args && server.args !== '[]' ? JSON.parse(server.args).join(' ') : ''}</span></p>}
                                    {server.auth_env_var && <p><span className="font-semibold text-gray-500">Auth env var:</span> <span className="font-mono text-gray-700">{server.auth_env_var}</span></p>}
                                    {install && <p className="text-gray-500 pt-1">{install.note}</p>}
                                </div>
                            )}
                        </div>
                    )}
                </div>
            </div>

            {tools && tools.length > 0 && (
                <div className="mt-3 pt-3 border-t">
                    <p className="text-xs font-semibold text-gray-500 mb-2">{tools.length} tool{tools.length !== 1 ? 's' : ''} available</p>
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-1.5">
                        {tools.map(tool => (
                            <div key={tool.name} className="bg-gray-50 rounded p-2">
                                <p className="text-xs font-mono font-semibold text-gray-800">{tool.name}</p>
                                {tool.description && <p className="text-xs text-gray-500 mt-0.5 truncate">{tool.description}</p>}
                            </div>
                        ))}
                    </div>
                </div>
            )}
        </div>
    );
}
