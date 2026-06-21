import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { Plus, Trash2, Edit2, Search, Power, Shield, Terminal, Globe, Cpu } from 'lucide-react';
import { useStore } from '../store';

interface MCPServer {
    id: number;
    company_id: number;
    name: string;
    display_name: string;
    description: string;
    transport: 'stdio' | 'http' | 'builtin';
    command: string;
    args: string;
    url: string;
    headers: string;
    auth_type: string;
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

const transportIcon = (t: string) => {
    if (t === 'stdio') return <Terminal size={14} className="inline mr-1" />;
    if (t === 'http') return <Globe size={14} className="inline mr-1" />;
    return <Cpu size={14} className="inline mr-1" />;
};

const emptyForm = {
    name: '', display_name: '', description: '',
    transport: 'stdio' as MCPServer['transport'],
    command: '', args: '[]', url: '', headers: '{}',
    auth_type: 'none', auth_token: '', enabled: true,
};

export const MCPServers: React.FC = () => {
    const { selectedCompanyId } = useStore();
    const [servers, setServers] = useState<MCPServer[]>([]);
    const [isModalOpen, setIsModalOpen] = useState(false);
    const [editingId, setEditingId] = useState<number | null>(null);
    const [formData, setFormData] = useState<typeof emptyForm>({ ...emptyForm });
    const [discoveredTools, setDiscoveredTools] = useState<Record<number, MCPTool[]>>({});
    const [discovering, setDiscovering] = useState<number | null>(null);
    const [error, setError] = useState<string | null>(null);

    const fetchServers = useCallback(async () => {
        if (!selectedCompanyId) return;
        try {
            const res = await axios.get(`/api/mcp-servers?company_id=${selectedCompanyId}`);
            setServers(res.data || []);
        } catch (e) {
            console.error(e);
        }
    }, [selectedCompanyId]);

    useEffect(() => { fetchServers(); }, [fetchServers]);

    const openModal = (s?: MCPServer) => {
        setError(null);
        if (s) {
            setEditingId(s.id);
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
            setFormData({ ...emptyForm });
        }
        setIsModalOpen(true);
    };

    const handleSave = async (e: React.FormEvent) => {
        e.preventDefault();
        setError(null);
        try {
            const payload = { ...formData, company_id: selectedCompanyId };
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
        try {
            const res = await axios.post(`/api/mcp-servers/${id}/discover`);
            setDiscoveredTools(prev => ({ ...prev, [id]: res.data.tools || [] }));
        } catch (e: any) {
            alert('Discovery failed: ' + (e.response?.data?.error || e.message));
        } finally {
            setDiscovering(null);
        }
    };

    return (
        <div className="h-full flex flex-col space-y-6">
            <div className="flex justify-between items-center">
                <div>
                    <h1 className="text-2xl font-bold">MCP Servers</h1>
                    <p className="text-sm text-gray-500 mt-1">Connect Model Context Protocol servers to extend agent capabilities.</p>
                </div>
                <button
                    onClick={() => openModal()}
                    className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700 shadow-sm"
                >
                    <Plus size={16} className="mr-2" /> Add MCP Server
                </button>
            </div>

            {servers.length === 0 && (
                <div className="text-center py-16 text-gray-400">
                    <Cpu size={48} className="mx-auto mb-4 opacity-30" />
                    <p className="text-lg">No MCP servers configured.</p>
                    <p className="text-sm mt-1">Add an MCP server to give agents access to external tools.</p>
                </div>
            )}

            <div className="grid grid-cols-1 gap-4">
                {servers.map(s => (
                    <div key={s.id} className={`bg-white rounded-lg border shadow-sm p-5 ${!s.enabled ? 'opacity-60' : ''}`}>
                        <div className="flex items-start justify-between">
                            <div className="flex-1 min-w-0">
                                <div className="flex items-center gap-2 mb-1">
                                    <h3 className="text-base font-semibold text-gray-900">{s.display_name || s.name}</h3>
                                    {s.builtin && (
                                        <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-indigo-100 text-indigo-700">
                                            <Shield size={10} className="mr-1" /> Built-in
                                        </span>
                                    )}
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
                                    title="Discover tools"
                                >
                                    <Search size={13} />
                                    {discovering === s.id ? 'Loading…' : 'Discover Tools'}
                                </button>
                                <button
                                    onClick={() => handleToggleEnabled(s)}
                                    className={`p-1.5 rounded ${s.enabled ? 'text-green-600 hover:text-green-800' : 'text-gray-400 hover:text-gray-600'}`}
                                    title={s.enabled ? 'Disable' : 'Enable'}
                                >
                                    <Power size={16} />
                                </button>
                                {!s.builtin && (
                                    <>
                                        <button onClick={() => openModal(s)} className="p-1.5 text-gray-500 hover:text-gray-700 rounded">
                                            <Edit2 size={16} />
                                        </button>
                                        <button onClick={() => handleDelete(s.id)} className="p-1.5 text-red-500 hover:text-red-700 rounded">
                                            <Trash2 size={16} />
                                        </button>
                                    </>
                                )}
                            </div>
                        </div>

                        {discoveredTools[s.id] && (
                            <div className="mt-3 pt-3 border-t">
                                <p className="text-xs font-semibold text-gray-500 mb-2">
                                    {discoveredTools[s.id].length} tool{discoveredTools[s.id].length !== 1 ? 's' : ''} available
                                </p>
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

            {isModalOpen && (
                <div className="fixed inset-0 bg-black/50 flex justify-center items-center z-50">
                    <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-lg max-h-[90vh] flex flex-col">
                        <h2 className="text-xl font-bold mb-4">{editingId ? 'Edit MCP Server' : 'Add MCP Server'}</h2>
                        <form onSubmit={handleSave} className="space-y-4 overflow-y-auto flex-1 pr-1">
                            <div className="grid grid-cols-2 gap-3">
                                <div>
                                    <label className="block text-sm font-medium text-gray-700 mb-1">Name (slug)</label>
                                    <input required type="text" value={formData.name}
                                        onChange={e => setFormData({ ...formData, name: e.target.value })}
                                        placeholder="github" className="w-full border rounded p-2 text-sm" />
                                </div>
                                <div>
                                    <label className="block text-sm font-medium text-gray-700 mb-1">Display Name</label>
                                    <input type="text" value={formData.display_name}
                                        onChange={e => setFormData({ ...formData, display_name: e.target.value })}
                                        placeholder="GitHub MCP" className="w-full border rounded p-2 text-sm" />
                                </div>
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
                                <input type="text" value={formData.description}
                                    onChange={e => setFormData({ ...formData, description: e.target.value })}
                                    placeholder="Access GitHub repositories, issues, and pull requests"
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
                                            placeholder="/usr/local/bin/github-mcp-server"
                                            className="w-full border rounded p-2 text-sm font-mono" />
                                    </div>
                                    <div>
                                        <label className="block text-sm font-medium text-gray-700 mb-1">Args (JSON array)</label>
                                        <input type="text" value={formData.args}
                                            onChange={e => setFormData({ ...formData, args: e.target.value })}
                                            placeholder='["--token", "your-token"]'
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
                                    <div>
                                        <label className="block text-sm font-medium text-gray-700 mb-1">Auth Type</label>
                                        <select value={formData.auth_type}
                                            onChange={e => setFormData({ ...formData, auth_type: e.target.value })}
                                            className="w-full border rounded p-2 text-sm">
                                            <option value="none">None</option>
                                            <option value="bearer">Bearer Token</option>
                                        </select>
                                    </div>
                                    {formData.auth_type === 'bearer' && (
                                        <div>
                                            <label className="block text-sm font-medium text-gray-700 mb-1">
                                                Bearer Token {editingId && '(leave blank to keep existing)'}
                                            </label>
                                            <input type="password" value={formData.auth_token}
                                                onChange={e => setFormData({ ...formData, auth_token: e.target.value })}
                                                className="w-full border rounded p-2 text-sm" />
                                        </div>
                                    )}
                                </>
                            )}

                            <div className="flex items-center gap-2">
                                <input type="checkbox" id="mcp-enabled" checked={formData.enabled}
                                    onChange={e => setFormData({ ...formData, enabled: e.target.checked })}
                                    className="h-4 w-4 text-indigo-600 border-gray-300 rounded" />
                                <label htmlFor="mcp-enabled" className="text-sm text-gray-700">Enabled</label>
                            </div>

                            {error && <p className="text-sm text-red-600">{error}</p>}
                        </form>
                        <div className="flex justify-end gap-3 pt-4 border-t mt-4">
                            <button type="button" onClick={() => setIsModalOpen(false)} className="text-gray-500 hover:text-gray-700 px-4 py-2 text-sm">Cancel</button>
                            <button onClick={handleSave} className="bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 text-sm">
                                {editingId ? 'Save Changes' : 'Add Server'}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};
