import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useStore, useIsOwner } from '../store';
import { SecretLabel } from '../components/SecretField';
import { Plus, Trash2, Edit2, Search, Power, Shield, Terminal, Globe, Cpu, Key, CheckCircle2, AlertCircle, GitBranch, FileText, ExternalLink, Share2, SearchIcon } from 'lucide-react';

interface MCPAccount {
    id: number;
    mcp_server_id: number;
    name: string;
    has_token: boolean;
    last_error: string;
    created_at: string;
    updated_at: string;
}

interface MCPServer {
    id: number;
    name: string;
    display_name: string;
    description: string;
    transport: 'stdio' | 'http' | 'builtin';
    command: string;
    args: string;
    deps: string; // JSON array of npm packages to pre-install
    url: string;
    headers: string;
    auth_type: string;
    auth_env_var: string;
    tools_cache: string;
    last_error: string;
    init_status: string;
    enabled: boolean;
    builtin: boolean;
    deps_installed: boolean;
    project_id: number | null;
    project?: { repository_url?: string; [key: string]: any };
    accounts: MCPAccount[];
    created_at: string;
    updated_at: string;
}

interface MCPTool {
    name: string;
    description: string;
    inputSchema?: any;
}

const HEADCOUNT1_TOOLS: MCPTool[] = [
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
    if (name === 'headcount1') return <Cpu size={20} />;
    if (name === 'postiz') return <Share2 size={20} />;
    if (name === 'brave-search') return <SearchIcon size={20} />;
    return <Globe size={20} />;
};

const authLabel = (authType: string) => {
    if (authType === 'bearer') return 'Personal Access Token';
	if (authType === 'github-app') return 'Connected GitHub App';
    if (authType === 'credentials-file') return 'Credentials file path';
    if (authType === 'url-token') return 'MCP URL (with your token)';
    return 'Token';
};

const emptyForm = {
    name: '', display_name: '', description: '',
    transport: 'stdio' as MCPServer['transport'],
    command: '', args: '[]', url: '', headers: '{}',
    auth_type: 'none', auth_token: '', enabled: true,
    // npm packages stored as newline-separated for the textarea, serialised to JSON on submit
    depsText: '',
};

export const MCPServers: React.FC = () => {
    const { selectedCompanyId } = useStore();
    const isOwner = useIsOwner();

    // ── Server modal state (for custom servers only) ───────────────────────────
    const [servers, setServers] = useState<MCPServer[]>([]);
    const [isModalOpen, setIsModalOpen] = useState(false);
    const [editingId, setEditingId] = useState<number | null>(null);
    const [formData, setFormData] = useState<typeof emptyForm>({ ...emptyForm });
    const [error, setError] = useState<string | null>(null);

    // ── Account modal state ─────────────────────────────────────────────────────
    type AccountModalMode = 'create' | 'reauth';
    const [accountModal, setAccountModal] = useState<{
        mode: AccountModalMode;
        serverId: number;
        serverDisplayName: string;
        authType: string;
        accountId: number | null;
        accountName: string;
    } | null>(null);
    const [accountForm, setAccountForm] = useState({ name: '', auth_token: '', credentials_json: '' });
    const [accountError, setAccountError] = useState<string | null>(null);

    // ── Google OAuth state ──────────────────────────────────────────────────────
    const [googleOAuth, setGoogleOAuth] = useState<{
        serverId: number;
        accountName: string;
        accountId: number | null;
        tokenPath: string;
    } | null>(null);
    const googleOAuthPollRef = React.useRef<ReturnType<typeof setInterval> | null>(null);

    const stopGoogleOAuthPoll = () => {
        if (googleOAuthPollRef.current) {
            clearInterval(googleOAuthPollRef.current);
            googleOAuthPollRef.current = null;
        }
    };

    // ── Tools discovery ─────────────────────────────────────────────────────────
    const [discoveredTools, setDiscoveredTools] = useState<Record<number, MCPTool[]>>({});
    const [discovering, setDiscovering] = useState<number | null>(null);  // account id
    const [discoverErrors, setDiscoverErrors] = useState<Record<number, string>>({});
    const [expandedTools, setExpandedTools] = useState<Record<number, boolean>>({});
    const toggleTools = (id: number) => setExpandedTools(prev => ({ ...prev, [id]: !prev[id] }));

    const fetchServers = useCallback(async () => {
        try {
            const url = selectedCompanyId ? `/api/mcp-servers?company_id=${selectedCompanyId}` : '/api/mcp-servers';
            const res = await axios.get(url);
            const data: MCPServer[] = res.data || [];
            setServers(data);
            const cached: Record<number, MCPTool[]> = {};
            for (const s of data) {
                if (s.name === 'headcount1') {
                    cached[s.id] = HEADCOUNT1_TOOLS;
                } else if (s.tools_cache) {
                    try { cached[s.id] = JSON.parse(s.tools_cache); } catch {}
                }
            }
            setDiscoveredTools(cached);
        } catch (e) {
            console.error(e);
        }
    }, [selectedCompanyId]);

    useEffect(() => { fetchServers(); }, [fetchServers]);

    // ── Custom server modal ─────────────────────────────────────────────────────
    const openModal = (s?: MCPServer) => {
        setError(null);
        if (s) {
            setEditingId(s.id);
            let depsText = '';
            try { depsText = JSON.parse(s.deps || '[]').join('\n'); } catch {}
            setFormData({
                name: s.name, display_name: s.display_name || '', description: s.description || '',
                transport: (s.transport === 'builtin' ? 'stdio' : s.transport) as MCPServer['transport'],
                command: s.command || '', args: s.args || '[]', url: s.url || '',
                headers: s.headers || '{}', auth_type: s.auth_type || 'none',
                auth_token: '', enabled: s.enabled, depsText,
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
        const { depsText, ...rest } = formData;
        const deps = JSON.stringify(
            depsText.split('\n').map(s => s.trim()).filter(Boolean)
        );
        const payload = { ...rest, deps };
        try {
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
            await axios.put(`/api/mcp-servers/${s.id}`, { ...s, enabled: !s.enabled });
            fetchServers();
        } catch (e) { console.error(e); }
    };

    // ── Account handlers ────────────────────────────────────────────────────────
    const openAddAccountModal = (s: MCPServer) => {
        setAccountError(null);
        setAccountForm({ name: '', auth_token: '', credentials_json: '' });
        setAccountModal({ mode: 'create', serverId: s.id, serverDisplayName: s.display_name, authType: s.auth_type, accountId: null, accountName: '' });
    };

    const openReauthModal = (s: MCPServer, acc: MCPAccount) => {
        setAccountError(null);
        setAccountForm({ name: acc.name, auth_token: '', credentials_json: '' });
        setAccountModal({ mode: 'reauth', serverId: s.id, serverDisplayName: s.display_name, authType: s.auth_type, accountId: acc.id, accountName: acc.name });
    };

    const handleSaveAccount = async () => {
        if (!accountModal) return;
        setAccountError(null);

		// OAuth integrations launch their provider flow instead of saving a manually-entered token.
		if (accountModal.authType === 'github-app') {
			try {
				const res = await axios.post(`/api/mcp-servers/${accountModal.serverId}/github-oauth`, {
					name: accountForm.name || 'GitHub account',
					account_id: accountModal.accountId || 0,
					return_path: window.location.pathname,
				});
				window.location.assign(res.data.authorize_url);
			} catch (e: any) {
				setAccountError(e.response?.data?.error || 'Failed to start GitHub authorization');
			}
			return;
		}

		// Google OAuth uses a separate two-step flow — start it instead of the normal save.
        if (accountModal.authType === 'google-oauth') {
            if (!accountForm.credentials_json) {
                setAccountError('Please select your OAuth client credentials JSON file.');
                return;
            }
            try {
                const res = await axios.post(`/api/mcp-servers/${accountModal.serverId}/google-oauth`, {
                    account_name: accountForm.name || 'Default',
                    oauth_keys_json: accountForm.credentials_json,
                    account_id: accountModal.accountId ?? 0,
                });
                const { token_path, account_name } = res.data;
                setAccountModal(null);
                setGoogleOAuth({ serverId: accountModal.serverId, accountName: account_name, accountId: accountModal.accountId, tokenPath: token_path });
                startGoogleOAuthPoll(accountModal.serverId, token_path, account_name, accountModal.accountId);
            } catch (e: any) {
                setAccountError(e.response?.data?.error || 'Failed to start OAuth flow');
            }
            return;
        }

        try {
            const payload: Record<string, string> = { name: accountForm.name || 'Default' };
            if (accountForm.credentials_json) {
                payload.credentials_json = accountForm.credentials_json;
            } else if (accountForm.auth_token) {
                payload.auth_token = accountForm.auth_token;
            }
            if (accountModal.mode === 'create') {
                await axios.post(`/api/mcp-servers/${accountModal.serverId}/accounts`, payload);
            } else {
                await axios.put(`/api/mcp-accounts/${accountModal.accountId}`, payload);
            }
            setAccountModal(null);
            await fetchServers();
            // Auto-trigger discovery for the server so tools cache + errors update immediately.
            handleDiscoverServer(accountModal.serverId);
        } catch (e: any) {
            setAccountError(e.response?.data?.error || 'Save failed');
        }
    };

    const handleDeleteAccount = async (accountId: number) => {
        if (!window.confirm('Remove this account?')) return;
        try {
            await axios.delete(`/api/mcp-accounts/${accountId}`);
            fetchServers();
        } catch (e: any) {
            alert(e.response?.data?.error || 'Delete failed');
        }
    };

    // Discover via a specific account (shows per-account errors).
    const handleDiscover = async (accountId: number, serverId: number) => {
        setDiscovering(accountId);
        setDiscoverErrors(prev => { const n = { ...prev }; delete n[accountId]; return n; });
        try {
            const res = await axios.post(`/api/mcp-accounts/${accountId}/discover`);
            setDiscoveredTools(prev => ({ ...prev, [serverId]: res.data.tools || [] }));
            await fetchServers();
        } catch (e: any) {
            const msg = e.response?.data?.error || e.message || 'Unknown error';
            setDiscoverErrors(prev => ({ ...prev, [accountId]: msg }));
            await fetchServers();
        } finally {
            setDiscovering(null);
        }
    };

    // Discover via the server endpoint (tries first account). Uses -serverId as discovering sentinel.
    const handleDiscoverServer = async (serverId: number) => {
        setDiscovering(-serverId);
        setDiscoverErrors(prev => { const n = { ...prev }; delete n[serverId]; return n; });
        try {
            const res = await axios.post(`/api/mcp-servers/${serverId}/discover`);
            setDiscoveredTools(prev => ({ ...prev, [serverId]: res.data.tools || [] }));
            await fetchServers();
        } catch (e: any) {
            const msg = e.response?.data?.error || e.message || 'Unknown error';
            setDiscoverErrors(prev => ({ ...prev, [serverId]: msg }));
            await fetchServers();
        } finally {
            setDiscovering(null);
        }
    };

    const startGoogleOAuthPoll = useCallback((serverId: number, tokenPath: string, accountName: string, accountId: number | null) => {
        stopGoogleOAuthPoll();
        googleOAuthPollRef.current = setInterval(async () => {
            try {
                const params = new URLSearchParams({ token_path: tokenPath, account_name: accountName });
                if (accountId) params.set('account_id', String(accountId));
                const res = await axios.get(`/api/mcp-servers/${serverId}/google-oauth?${params}`);
                if (res.data.ready) {
                    stopGoogleOAuthPoll();
                    setGoogleOAuth(null);
                    await fetchServers();
                    handleDiscoverServer(serverId);
                }
            } catch (e) {
                stopGoogleOAuthPoll();
                setGoogleOAuth(null);
            }
        }, 2000);
    }, [fetchServers, handleDiscoverServer]); // eslint-disable-line react-hooks/exhaustive-deps

    // Split servers: headcount1 first, then other predefined, then custom
    const headcount1 = servers.find(s => s.name === 'headcount1');
    const predefined = servers.filter(s => s.builtin && s.name !== 'headcount1');
    const custom = servers.filter(s => !s.builtin);

    return (
        <div className="h-full flex flex-col space-y-6">
            <div className="flex justify-between items-center">
                <div>
                    <h1 className="text-2xl font-bold">MCP Servers</h1>
                    <p className="text-sm text-gray-500 mt-1">Connect MCP servers to extend agent capabilities.</p>
                </div>
                <button
                    onClick={() => openModal()}
                    className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700 shadow-sm"
                >
                    <Plus size={16} className="mr-2" /> Add MCP Server
                </button>
            </div>

            {/* Built-in headcount1 — always on */}
            {headcount1 && (
                <section>
                    <h2 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">Built-in</h2>
                    <div className="bg-white rounded-lg border border-indigo-100 shadow-sm p-5">
                        <div className="flex items-start gap-3">
                            <div className="p-2 bg-indigo-50 rounded-lg text-indigo-600 flex-shrink-0">
                                {serverIcon(headcount1.name)}
                            </div>
                            <div className="flex-1 min-w-0">
                                <div className="flex items-center gap-2 mb-1">
                                    <h3 className="text-base font-semibold text-gray-900">{headcount1.display_name}</h3>
                                    <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-indigo-100 text-indigo-700">
                                        <Shield size={10} className="mr-1" /> Always On
                                    </span>
                                </div>
                                <p className="text-sm text-gray-600 mb-3">{headcount1.description}</p>
                                <div className="grid grid-cols-1 sm:grid-cols-2 gap-1.5">
                                    {HEADCOUNT1_TOOLS.map(tool => (
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
                                discovering={discovering}
                                discoverErrors={discoverErrors}
                                onAddAccount={() => openAddAccountModal(s)}
                                onReauthAccount={(acc) => openReauthModal(s, acc)}
                                onDeleteAccount={handleDeleteAccount}
                                onDiscoverAccount={(accId) => handleDiscover(accId, s.id)}
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
                        {custom.map(s => {
                            const isCodegraph = !!s.project_id;
                            const cgReady = s.init_status === 'ready';
                            const cgInitializing = s.init_status === 'initializing';
                            const cgError = s.init_status?.startsWith('error:');
                            const borderCls = isCodegraph
                                ? (cgReady ? 'border-green-200' : cgInitializing ? 'border-amber-200' : cgError ? 'border-red-200' : 'border-gray-200')
                                : (!s.enabled ? 'border-gray-200 opacity-60' : 'border-gray-200');
                            return (
                            <div key={s.id} className={`bg-white rounded-lg border shadow-sm p-5 ${borderCls}`}>
                                <div className="flex items-start justify-between">
                                    <div className="flex-1 min-w-0">
                                        <div className="flex items-center gap-2 mb-1">
                                            <h3 className="text-base font-semibold text-gray-900">{s.display_name || s.name}</h3>
                                            {isCodegraph ? (
                                                cgReady ? (
                                                    <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-green-100 text-green-700">
                                                        <CheckCircle2 size={10} /> Ready
                                                    </span>
                                                ) : cgInitializing ? (
                                                    <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-amber-100 text-amber-700">
                                                        <AlertCircle size={10} /> Initializing…
                                                    </span>
                                                ) : cgError ? (
                                                    <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-red-100 text-red-700">
                                                        <AlertCircle size={10} /> Init failed
                                                    </span>
                                                ) : (
                                                    <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-500">
                                                        <AlertCircle size={10} /> Not initialized
                                                    </span>
                                                )
                                            ) : (
                                                <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${s.enabled ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-500'}`}>
                                                    {s.enabled ? 'Enabled' : 'Disabled'}
                                                </span>
                                            )}
                                            <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-600">
                                                {transportIcon(s.transport)}{s.transport}
                                            </span>
                                        </div>
                                        {s.description && <p className="text-sm text-gray-600 mb-2">{s.description}</p>}
                                        {s.project?.repository_url && (
                                            <p className="text-xs text-gray-400 font-mono truncate mb-1">{s.project.repository_url}</p>
                                        )}
                                        {s.transport === 'stdio' && s.command && (
                                            <p className="text-xs text-gray-400 font-mono truncate">$ {s.command} {s.args && s.args !== '[]' ? JSON.parse(s.args).join(' ') : ''}</p>
                                        )}
                                        {s.transport === 'http' && s.url && (
                                            <p className="text-xs text-gray-400 font-mono truncate">{s.url}</p>
                                        )}
                                    </div>
                                    <div className="flex items-center gap-2 ml-4 flex-shrink-0">
                                        <button
                                            onClick={() => handleDiscoverServer(s.id)}
                                            disabled={discovering === -s.id}
                                            className="text-blue-500 hover:text-blue-700 px-2 py-1 text-xs border border-blue-200 rounded hover:bg-blue-50 flex items-center gap-1"
                                        >
                                            <Search size={13} />
                                            {discovering === -s.id ? 'Loading…' : 'Discover'}
                                        </button>
                                        <button onClick={() => handleToggleEnabled(s)} className={`p-1.5 rounded ${s.enabled ? 'text-green-600 hover:text-green-800' : 'text-gray-400 hover:text-gray-600'}`} title={s.enabled ? 'Disable' : 'Enable'}>
                                            <Power size={16} />
                                        </button>
                                        <button onClick={() => openModal(s)} className="p-1.5 text-gray-500 hover:text-gray-700 rounded">
                                            <Edit2 size={16} />
                                        </button>
                                        {isOwner && (
                                            <button onClick={() => handleDelete(s.id)} className="p-1.5 text-red-500 hover:text-red-700 rounded">
                                                <Trash2 size={16} />
                                            </button>
                                        )}
                                    </div>
                                </div>
                                {discoverErrors[s.id] && (
                                    <div className="mt-2 text-xs text-red-600 bg-red-50 rounded p-2">{discoverErrors[s.id]}</div>
                                )}
                                {discoveredTools[s.id] && (
                                    <div className="mt-3 pt-3 border-t">
                                        <button onClick={() => toggleTools(s.id)}
                                            className="flex items-center gap-1.5 text-xs font-semibold text-gray-500 hover:text-gray-700 w-full text-left">
                                            <span>{discoveredTools[s.id].length} tool{discoveredTools[s.id].length !== 1 ? 's' : ''} available</span>
                                            <span className="text-gray-400">{expandedTools[s.id] ? '▲' : '▼'}</span>
                                        </button>
                                        {expandedTools[s.id] && (
                                            <div className="grid grid-cols-1 sm:grid-cols-2 gap-1.5 mt-2">
                                                {discoveredTools[s.id].map(tool => (
                                                    <div key={tool.name} className="bg-gray-50 rounded p-2">
                                                        <p className="text-xs font-mono font-semibold text-gray-800">{tool.name}</p>
                                                        {tool.description && <p className="text-xs text-gray-500 mt-0.5 truncate">{tool.description}</p>}
                                                    </div>
                                                ))}
                                            </div>
                                        )}
                                    </div>
                                )}
                            </div>
                        );
                        })}
                    </div>
                </section>
            )}

            {servers.length === 0 && (
                <div className="text-center py-16 text-gray-400">
                    <Cpu size={48} className="mx-auto mb-4 opacity-30" />
                    <p className="text-lg">No MCP servers configured.</p>
                </div>
            )}

            {/* Google OAuth waiting banner */}
            {googleOAuth && (
                <div className="fixed bottom-6 left-1/2 -translate-x-1/2 bg-white border border-indigo-200 shadow-lg rounded-lg px-5 py-3 flex items-center gap-3 z-50 max-w-md w-full mx-4">
                    <div className="w-4 h-4 rounded-full border-2 border-indigo-500 border-t-transparent animate-spin flex-shrink-0" />
                    <div className="flex-1 min-w-0">
                        <p className="text-sm font-medium text-gray-900">Waiting for Google sign-in…</p>
                        <p className="text-xs text-gray-500 truncate">Complete authorization in the browser that just opened.</p>
                    </div>
                    <button onClick={() => { stopGoogleOAuthPoll(); setGoogleOAuth(null); }} className="text-xs text-gray-400 hover:text-gray-600 flex-shrink-0">Cancel</button>
                </div>
            )}

            {/* Custom server modal */}
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
                                    <div>
                                        <label className="block text-sm font-medium text-gray-700 mb-1">
                                            npm packages to install
                                        </label>
                                        <textarea
                                            value={formData.depsText}
                                            onChange={e => setFormData({ ...formData, depsText: e.target.value })}
                                            placeholder={"@modelcontextprotocol/server-github\n@my-org/my-mcp-server"}
                                            rows={3}
                                            className="w-full border rounded p-2 text-sm font-mono resize-none"
                                        />
                                        <p className="text-xs text-gray-400 mt-1">One package per line. Installed globally via npm when the server is saved.</p>
                                    </div>
                                </>
                            )}
                            {formData.transport === 'http' && (
                                <div>
                                    <label className="block text-sm font-medium text-gray-700 mb-1">Server URL</label>
                                    <input required type="url" value={formData.url}
                                        onChange={e => setFormData({ ...formData, url: e.target.value })}
                                        placeholder="https://mcp.example.com"
                                        className="w-full border rounded p-2 text-sm" />
                                </div>
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
                                    <SecretLabel>
                                        {authLabel(formData.auth_type)} {editingId && '(leave blank to keep existing)'}
                                    </SecretLabel>
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

            {/* Account modal (authorize / re-authenticate a predefined server account) */}
            {accountModal && (
                <div className="fixed inset-0 bg-black/50 flex justify-center items-center z-50">
                    <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-md">
                        <h2 className="text-xl font-bold mb-1">
                            {accountModal.mode === 'create'
                                ? `Authorize ${accountModal.serverDisplayName}`
                                : `Re-authenticate "${accountModal.accountName}"`}
                        </h2>
                        {accountModal.mode === 'create' && (
                            <p className="text-sm text-gray-500 mb-4">This will create a new connected account for {accountModal.serverDisplayName}.</p>
                        )}
                        <div className="space-y-4 mt-4">
                            {accountModal.mode === 'create' && (
                                <div>
                                    <label className="block text-sm font-medium text-gray-700 mb-1">Account name</label>
                                    <input type="text" value={accountForm.name}
                                        onChange={e => setAccountForm(f => ({ ...f, name: e.target.value }))}
                                        placeholder="Default"
                                        className="w-full border rounded p-2 text-sm" />
                                    <p className="text-xs text-gray-400 mt-1">Use a descriptive name if you plan to add multiple accounts.</p>
                                </div>
                            )}
                            {accountModal.authType === 'google-oauth' ? (
                                <div>
                                    <SecretLabel>OAuth client credentials JSON</SecretLabel>
                                    <label className={`flex items-center gap-2 w-full border-2 border-dashed rounded p-3 cursor-pointer transition-colors ${accountForm.credentials_json ? 'border-green-300 bg-green-50' : 'border-gray-200 hover:border-indigo-300 hover:bg-indigo-50'}`}>
                                        <input type="file" accept=".json,application/json" className="hidden"
                                            onChange={e => {
                                                const file = e.target.files?.[0];
                                                if (!file) return;
                                                const reader = new FileReader();
                                                reader.onload = ev => setAccountForm(f => ({ ...f, credentials_json: ev.target?.result as string || '' }));
                                                reader.readAsText(file);
                                            }} />
                                        {accountForm.credentials_json ? (
                                            <span className="text-sm text-green-700 flex items-center gap-1.5">
                                                <CheckCircle2 size={15} /> OAuth credentials file loaded
                                            </span>
                                        ) : (
                                            <span className="text-sm text-gray-500">Click to select OAuth credentials JSON file</span>
                                        )}
                                    </label>
                                    <p className="text-xs text-gray-400 mt-1.5">
                                        In{' '}
                                        <a href="https://console.cloud.google.com" target="_blank" rel="noopener noreferrer" className="text-indigo-600 hover:underline inline-flex items-center gap-0.5">
                                            Google Cloud Console<ExternalLink size={10} className="ml-0.5" />
                                        </a>
                                        {' '}→ APIs & Services → Credentials, create an <strong>OAuth 2.0 Client ID</strong> (Desktop app type) and download the JSON. A browser will open for sign-in after you click Authorize.
                                    </p>
                                </div>
                            ) : accountModal.authType === 'credentials-file' ? (
                                <div>
                                    <SecretLabel>
                                        Credentials JSON file
                                        {accountModal.mode === 'reauth' && ' (leave blank to keep existing)'}
                                    </SecretLabel>
                                    <label className={`flex items-center gap-2 w-full border-2 border-dashed rounded p-3 cursor-pointer transition-colors ${accountForm.credentials_json ? 'border-green-300 bg-green-50' : 'border-gray-200 hover:border-indigo-300 hover:bg-indigo-50'}`}>
                                        <input type="file" accept=".json,application/json" className="hidden"
                                            onChange={e => {
                                                const file = e.target.files?.[0];
                                                if (!file) return;
                                                const reader = new FileReader();
                                                reader.onload = ev => setAccountForm(f => ({ ...f, credentials_json: ev.target?.result as string || '' }));
                                                reader.readAsText(file);
                                            }} />
                                        {accountForm.credentials_json ? (
                                            <span className="text-sm text-green-700 flex items-center gap-1.5">
                                                <CheckCircle2 size={15} /> Credentials file loaded
                                            </span>
                                        ) : (
                                            <span className="text-sm text-gray-500">Click to select credentials JSON file</span>
                                        )}
                                    </label>
                                    <p className="text-xs text-gray-400 mt-1.5">
                                        Create a service account in{' '}
                                        <a href="https://console.cloud.google.com" target="_blank" rel="noopener noreferrer" className="text-indigo-600 hover:underline inline-flex items-center gap-0.5">
                                            Google Cloud Console<ExternalLink size={10} className="ml-0.5" />
                                        </a>
                                        {' '}and download the JSON key file.
                                    </p>
                                </div>
							) : accountModal.authType === 'github-app' ? (
								<div className="rounded-md border border-indigo-100 bg-indigo-50 p-3 text-sm text-indigo-950"><p className="font-medium">Sign in with GitHub</p><p className="mt-1 text-indigo-800">Authorize this account in GitHub, choose the repositories Headcount1 may access, then return here. You can add multiple personal or work accounts.</p></div>
                            ) : accountModal.authType !== 'none' ? (
                                <div>
                                    <SecretLabel>
                                        {authLabel(accountModal.authType)}
                                        {accountModal.mode === 'reauth' && ' (leave blank to keep existing)'}
                                    </SecretLabel>
                                    <input type={accountModal.authType === 'url-token' ? 'text' : 'password'} value={accountForm.auth_token}
                                        onChange={e => setAccountForm(f => ({ ...f, auth_token: e.target.value }))}
                                        placeholder={accountModal.authType === 'url-token' ? 'https://mcp.postiz.com/mcp/...' : ''}
                                        className="w-full border rounded p-2 text-sm font-mono" />
                                    {accountModal.serverDisplayName === 'Postiz' && (
                                        <p className="text-xs text-gray-400 mt-1">
                                            In your Postiz dashboard go to <strong>Settings → MCP</strong>, copy the full personal URL (looks like <code>https://mcp.postiz.com/mcp/...</code>) and paste it above.
                                        </p>
                                    )}
                                    {accountModal.serverDisplayName === 'Brave Search' && (
                                        <p className="text-xs text-gray-400 mt-1">
                                            Get your API key at{' '}
                                            <a href="https://brave.com/search/api/" target="_blank" rel="noopener noreferrer" className="text-indigo-600 hover:underline inline-flex items-center gap-0.5">
                                                brave.com/search/api<ExternalLink size={10} className="ml-0.5" />
                                            </a>
                                            . Free tier includes 2,000 queries/month.
                                        </p>
                                    )}
                                </div>
                            ) : null}
                            {accountError && <p className="text-sm text-red-600">{accountError}</p>}
                        </div>
                        <div className="flex justify-end gap-3 pt-4 border-t mt-4">
                            <button type="button" onClick={() => setAccountModal(null)} className="text-gray-500 hover:text-gray-700 px-4 py-2 text-sm">Cancel</button>
                            <button onClick={handleSaveAccount} className="bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 text-sm">
                                {accountModal.mode === 'create' ? 'Authorize' : 'Re-authenticate'}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};

interface SetupStep {
    before?: string;
    link?: { text: string; url: string };
    after?: string;
    code?: string;
}

const SETUP_INSTRUCTIONS: Record<string, { steps: SetupStep[]; docsUrl: string; docsLabel: string }> = {
github: {
        docsUrl: 'https://github.com/github/github-mcp-server',
        docsLabel: 'github-mcp-server on GitHub',
        steps: [
            { before: 'Install the MCP server binary:', code: 'brew install github-mcp-server' },
			{ before: 'Click Authorize below and sign in to GitHub. No personal access token is required.' },
			{ before: 'Choose the repositories Headcount1 may access in the GitHub App installation screen.' },
			{ before: 'Add another account whenever you need separate personal and work access.' },
        ],
    },
    'brave-search': {
        docsUrl: 'https://brave.com/search/api/',
        docsLabel: 'Brave Search API',
        steps: [
            { before: 'Go to ', link: { text: 'brave.com/search/api', url: 'https://brave.com/search/api/' }, after: ' and sign up for a Brave Search API key.' },
            { before: 'Choose a plan (Free tier includes 2,000 searches/month).' },
            { before: 'Click Authorize below and paste your API key.' },
        ],
    },
    postiz: {
        docsUrl: 'https://postiz.com/mcp',
        docsLabel: 'Postiz MCP page',
        steps: [
            { before: 'Log in to ', link: { text: 'postiz.com', url: 'https://postiz.com' }, after: ' and go to Settings → MCP.' },
            { before: 'Copy your personal MCP URL (starts with https://mcp.postiz.com/mcp/...).' },
            { before: 'Click Authorize below and paste the full URL.' },
        ],
    },
    'google-docs': {
        docsUrl: 'https://developers.google.com/drive/api/quickstart/nodejs',
        docsLabel: 'Google Drive API quickstart',
        steps: [
            { before: 'Open ', link: { text: 'Google Cloud Console', url: 'https://console.cloud.google.com' }, after: ', create or select a project, and enable the Google Drive API.' },
            { before: 'Go to APIs & Services → Credentials, click Create Credentials → OAuth 2.0 Client ID, choose Desktop app, and download the JSON.' },
            { before: 'Click Authorize below, upload the downloaded JSON, and complete Google sign-in in the browser that opens.' },
        ],
    },
};

// Card for pre-defined integrations (github, google-docs, etc.)
function PredefinedServerCard({ server, tools, discovering, discoverErrors, onAddAccount, onReauthAccount, onDeleteAccount, onDiscoverAccount }: {
    server: MCPServer;
    tools?: MCPTool[];
    discovering: number | null;
    discoverErrors: Record<number, string>;
    onAddAccount: () => void;
    onReauthAccount: (acc: MCPAccount) => void;
    onDeleteAccount: (accountId: number) => void;
    onDiscoverAccount: (accountId: number) => void;
}) {
    const [showDetails, setShowDetails] = React.useState(false);
    const [showTools, setShowTools] = React.useState(false);
    const [showSetup, setShowSetup] = React.useState(false);
    const hasAccounts = server.accounts && server.accounts.length > 0;
    const isConnected = hasAccounts && !!server.tools_cache;
    const setup = SETUP_INSTRUCTIONS[server.name];

    const borderClass = isConnected ? 'border-green-200' : hasAccounts ? 'border-amber-200' : 'border-gray-200';
    const iconClass = isConnected ? 'bg-green-50 text-green-700' : hasAccounts ? 'bg-amber-50 text-amber-600' : 'bg-gray-100 text-gray-500';

    return (
        <div className={`bg-white rounded-lg border shadow-sm p-5 ${borderClass}`}>
            <div className="flex items-start gap-3">
                <div className={`p-2 rounded-lg flex-shrink-0 ${iconClass}`}>
                    {serverIcon(server.name)}
                </div>
                <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 mb-1 flex-wrap">
                        <h3 className="text-base font-semibold text-gray-900">{server.display_name}</h3>
                        <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-500">
                            <Shield size={10} className="mr-1" /> Built-in
                        </span>
                        {isConnected ? (
                            <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-green-100 text-green-700">
                                <CheckCircle2 size={10} /> Connected
                            </span>
                        ) : hasAccounts ? (
                            <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-amber-100 text-amber-700">
                                <AlertCircle size={10} /> Setup required
                            </span>
                        ) : (
                            <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-500">
                                <AlertCircle size={10} /> Not connected
                            </span>
                        )}
                    </div>
                    <p className="text-sm text-gray-600 mb-3">{server.description}</p>

                    {!hasAccounts ? (
                        /* No accounts — compact by default, setup steps on demand */
                        <div className="space-y-2">
                            <div className="flex items-center gap-2 flex-wrap">
                                <button onClick={onAddAccount}
                                    className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-indigo-600 text-white text-sm rounded hover:bg-indigo-700">
                                    <Key size={13} /> Authorize
                                </button>
                                {setup && (
                                    <button onClick={() => setShowSetup(v => !v)}
                                        className="text-xs text-gray-500 hover:text-gray-700 px-2 py-1.5 border border-gray-200 rounded flex items-center gap-1">
                                        {showSetup ? 'Hide setup steps' : 'Setup steps'}
                                        <span className="text-gray-400 text-[10px]">{showSetup ? '▲' : '▼'}</span>
                                    </button>
                                )}
                            </div>
                            {showSetup && setup && (
                                <div className="bg-gray-50 rounded-lg border border-gray-100 p-4 text-xs space-y-3">
                                    <ol className="space-y-2.5">
                                        {setup.steps.map((step, i) => {
                                            const isInstallStep = !!step.code;
                                            const done = isInstallStep && server.deps_installed;
                                            return (
                                                <li key={i} className={`space-y-1 ${done ? 'opacity-70' : ''}`}>
                                                    <p className={`flex gap-2 ${done ? 'text-green-700' : 'text-gray-700'}`}>
                                                        <span className={`flex-shrink-0 inline-flex items-center justify-center w-4 h-4 rounded-full font-bold text-[10px] mt-0.5 ${done ? 'bg-green-100 text-green-700' : 'bg-indigo-100 text-indigo-700'}`}>
                                                            {done ? <CheckCircle2 size={10} /> : i + 1}
                                                        </span>
                                                        <span>
                                                            {step.before}
                                                            {step.link && (
                                                                <a href={step.link.url} target="_blank" rel="noopener noreferrer"
                                                                    className="text-indigo-600 hover:underline inline-flex items-center gap-0.5">
                                                                    {step.link.text}<ExternalLink size={10} className="ml-0.5" />
                                                                </a>
                                                            )}
                                                            {step.after}
                                                            {done && <span className="ml-1.5 text-green-600 font-semibold">Installed</span>}
                                                        </span>
                                                    </p>
                                                    {step.code && !done && (
                                                        <p className="font-mono text-gray-700 bg-gray-200 rounded px-2 py-1 ml-6">{step.code}</p>
                                                    )}
                                                </li>
                                            );
                                        })}
                                    </ol>
                                    <a href={setup.docsUrl} target="_blank" rel="noopener noreferrer"
                                        className="inline-flex items-center gap-1 text-indigo-500 hover:text-indigo-700 hover:underline text-[11px] pt-1">
                                        <ExternalLink size={11} /> {setup.docsLabel}
                                    </a>
                                </div>
                            )}
                        </div>
                    ) : (
                        /* Has accounts — show each account as a row */
                        <div className="space-y-2">
                            <div className="space-y-1.5">
                                {server.accounts.map(acc => {
                                    const accError = discoverErrors[acc.id] || acc.last_error;
                                    const isDiscovering = discovering === acc.id;
                                    return (
                                        <div key={acc.id} className={`rounded border p-2.5 ${accError ? 'border-amber-200 bg-amber-50' : 'border-gray-100 bg-gray-50'}`}>
                                            <div className="flex items-center justify-between gap-2">
                                                <div className="flex items-center gap-2 min-w-0">
                                                    <span className={`w-2 h-2 rounded-full flex-shrink-0 ${acc.has_token ? 'bg-green-400' : 'bg-gray-300'}`} title={acc.has_token ? 'Token stored' : 'No token'} />
                                                    <span className="text-sm font-medium text-gray-800 truncate">{acc.name}</span>
                                                    {!acc.has_token && (
                                                        <span className="text-xs text-amber-600 flex-shrink-0">No token</span>
                                                    )}
                                                </div>
                                                <div className="flex items-center gap-1.5 flex-shrink-0">
                                                    <button onClick={() => onDiscoverAccount(acc.id)} disabled={isDiscovering}
                                                        className="text-blue-500 hover:text-blue-700 px-2 py-0.5 text-xs border border-blue-200 rounded hover:bg-blue-50 flex items-center gap-1">
                                                        <Search size={12} />
                                                        {isDiscovering ? 'Loading…' : 'Discover'}
                                                    </button>
                                                    <button onClick={() => onReauthAccount(acc)}
                                                        className="text-xs text-gray-600 hover:text-gray-900 px-2 py-0.5 border border-gray-200 rounded flex items-center gap-1">
                                                        <Key size={11} /> Re-auth
                                                    </button>
                                                    <button onClick={() => onDeleteAccount(acc.id)}
                                                        className="text-xs text-red-400 hover:text-red-600 px-1.5 py-0.5 border border-red-100 rounded flex items-center">
                                                        <Trash2 size={11} />
                                                    </button>
                                                </div>
                                            </div>
                                            {accError && (
                                                <p className="text-xs text-amber-800 mt-1.5 leading-relaxed">{accError}</p>
                                            )}
                                        </div>
                                    );
                                })}
                            </div>
                            <div className="flex items-center gap-2 flex-wrap pt-1">
                                <button onClick={onAddAccount}
                                    className="text-xs text-indigo-600 hover:text-indigo-800 px-2 py-1 border border-indigo-200 rounded flex items-center gap-1 hover:bg-indigo-50">
                                    <Plus size={12} /> Add account
                                </button>
                                <button onClick={() => setShowDetails(v => !v)}
                                    className="text-xs text-gray-500 hover:text-gray-700 px-2 py-1 border border-gray-200 rounded flex items-center gap-1">
                                    <Edit2 size={12} /> {showDetails ? 'Hide details' : 'Details'}
                                </button>
                            </div>
                            {showDetails && (
                                <div className="bg-gray-50 rounded p-3 text-xs space-y-1 border border-gray-100">
                                    {server.command && <p><span className="font-semibold text-gray-500">Command:</span> <span className="font-mono text-gray-700">{server.command} {server.args && server.args !== '[]' ? JSON.parse(server.args).join(' ') : ''}</span></p>}
                                    {server.auth_env_var && <p><span className="font-semibold text-gray-500">Auth env var:</span> <span className="font-mono text-gray-700">{server.auth_env_var}</span></p>}
                                    {setup && <a href={setup.docsUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-indigo-500 hover:underline text-[11px] pt-1"><ExternalLink size={10} /> {setup.docsLabel}</a>}
                                </div>
                            )}
                        </div>
                    )}
                </div>
            </div>

            {tools && tools.length > 0 && (
                <div className="mt-3 pt-3 border-t">
                    <button onClick={() => setShowTools(v => !v)}
                        className="flex items-center gap-1.5 text-xs font-semibold text-gray-500 hover:text-gray-700 w-full text-left">
                        <span>{tools.length} tool{tools.length !== 1 ? 's' : ''} available</span>
                        <span className="text-gray-400">{showTools ? '▲' : '▼'}</span>
                    </button>
                    {showTools && (
                        <div className="grid grid-cols-1 sm:grid-cols-2 gap-1.5 mt-2">
                            {tools.map(tool => (
                                <div key={tool.name} className="bg-gray-50 rounded p-2">
                                    <p className="text-xs font-mono font-semibold text-gray-800">{tool.name}</p>
                                    {tool.description && <p className="text-xs text-gray-500 mt-0.5 truncate">{tool.description}</p>}
                                </div>
                            ))}
                        </div>
                    )}
                </div>
            )}
        </div>
    );
}
