/* eslint-disable @typescript-eslint/no-explicit-any */
import { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import { Plus, Trash2, Lock, RefreshCw, Link2, CheckCircle2, AlertCircle } from 'lucide-react';
import { SecretField, SecretLabel } from './SecretField';

interface EnvEntry {
    id: number;
    name: string;
    kind: 'secret' | 'variable';
    has_value: boolean;
}

interface Connector {
    id: number;
    provider: 'vercel' | 'github';
    target_project: string;
    target_environment: string;
    team_id: string;
    has_token: boolean;
    last_sync_at: string | null;
    last_sync_status: '' | 'ok' | 'error';
    last_sync_error: string;
}

interface ProjectEnv {
    id: number;
    name: string;
    secrets: EnvEntry[];
    connectors?: Connector[];
}

interface RemoteEntry {
    name: string;
    kind: 'secret' | 'variable';
}

interface RemoteState {
    entries: RemoteEntry[];
    errors?: { provider: string; error: string }[];
}

// Deploy environments of a project: named sets of secrets + variables (all
// encrypted at rest with the user's passkey key), optionally connected to
// Vercel project environments or GitHub repo environments. Every change is
// pushed to connected targets so the next deployment picks up current values.
export function ProjectEnvironments({ projectId }: { projectId: number }) {
    const [envs, setEnvs] = useState<ProjectEnv[]>([]);
    const [error, setError] = useState('');
    const [newEnvName, setNewEnvName] = useState('');
    const [entryForms, setEntryForms] = useState<Record<number, { name: string; value: string; kind: 'secret' | 'variable' }>>({});
    const [syncing, setSyncing] = useState<number | null>(null);
    // Connector wizard state, one open wizard at a time (per env).
    const [wizardEnv, setWizardEnv] = useState<number | null>(null);
    const [wizard, setWizard] = useState<{
        provider: 'vercel' | 'github';
        token: string;
        teamId: string;
        projects: { id: string; name: string }[];
        project: string;
        environments: { id: string; name: string }[];
        environment: string;
        loading: boolean;
    }>({ provider: 'vercel', token: '', teamId: '', projects: [], project: '', environments: [], environment: '', loading: false });

    // Live view of what exists on the connected targets, keyed by env id.
    // Pulled fresh on every load so the UI always reflects the target state.
    const [remote, setRemote] = useState<Record<number, RemoteState>>({});

    const load = useCallback(async () => {
        try {
            const res = await axios.get(`/api/projects/${projectId}/environments`);
            const list: ProjectEnv[] = res.data || [];
            setEnvs(list);
            // Pull remote entry names for every connected environment.
            list.filter((e) => (e.connectors?.length ?? 0) > 0).forEach(async (e) => {
                try {
                    const rr = await axios.get(`/api/environments/${e.id}/remote-entries`);
                    setRemote((s) => ({ ...s, [e.id]: rr.data }));
                } catch {
                    /* remote view is best-effort; local data still renders */
                }
            });
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to load environments');
        }
    }, [projectId]);

    useEffect(() => { load(); }, [load]);

    const createEnv = async () => {
        if (!newEnvName.trim()) return;
        try {
            await axios.post(`/api/projects/${projectId}/environments`, { name: newEnvName.trim() });
            setNewEnvName('');
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to create environment');
        }
    };

    const deleteEnv = async (env: ProjectEnv) => {
        if (!confirm(`Delete environment "${env.name}", its secrets and connectors? Values already pushed to targets remain there.`)) return;
        try {
            await axios.delete(`/api/environments/${env.id}`);
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to delete environment');
        }
    };

    const saveEntry = async (env: ProjectEnv) => {
        const form = entryForms[env.id];
        if (!form?.name?.trim() || !form?.value) return;
        try {
            await axios.put(`/api/environments/${env.id}/secrets`, {
                name: form.name.trim(),
                value: form.value,
                kind: form.kind,
            });
            setEntryForms((s) => ({ ...s, [env.id]: { name: '', value: '', kind: form.kind } }));
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to save');
        }
    };

    const deleteEntry = async (env: ProjectEnv, name: string) => {
        if (!confirm(`Delete ${name} from "${env.name}" (and from connected targets)?`)) return;
        try {
            await axios.delete(`/api/environments/${env.id}/secrets/${encodeURIComponent(name)}`);
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to delete');
        }
    };

    const syncNow = async (env: ProjectEnv) => {
        setSyncing(env.id);
        try {
            await axios.post(`/api/environments/${env.id}/sync`);
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Sync failed');
        } finally {
            setSyncing(null);
        }
    };

    const deleteConnector = async (id: number) => {
        if (!confirm('Disconnect this deploy target? Values already pushed remain on the target.')) return;
        try {
            await axios.delete(`/api/connectors/${id}`);
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to disconnect');
        }
    };

    // Wizard: token → list projects/repos → pick one → list environments.
    const discoverProjects = async () => {
        setWizard((s) => ({ ...s, loading: true }));
        try {
            const res = await axios.post('/api/connector-discovery', {
                provider: wizard.provider, token: wizard.token, team_id: wizard.teamId,
            });
            setWizard((s) => ({ ...s, projects: res.data.projects || [], project: '', environments: [], environment: '', loading: false }));
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Could not list projects — check the token');
            setWizard((s) => ({ ...s, loading: false }));
        }
    };

    const discoverEnvironments = async (project: string) => {
        setWizard((s) => ({ ...s, project, loading: true }));
        try {
            const res = await axios.post('/api/connector-discovery', {
                provider: wizard.provider, token: wizard.token, team_id: wizard.teamId, project,
            });
            setWizard((s) => ({ ...s, environments: res.data.environments || [], environment: '', loading: false }));
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Could not list environments');
            setWizard((s) => ({ ...s, loading: false }));
        }
    };

    const createConnector = async (env: ProjectEnv) => {
        try {
            await axios.post(`/api/environments/${env.id}/connectors`, {
                provider: wizard.provider,
                token: wizard.token,
                team_id: wizard.teamId,
                target_project: wizard.project,
                target_environment: wizard.environment,
            });
            setWizardEnv(null);
            setWizard({ provider: 'vercel', token: '', teamId: '', projects: [], project: '', environments: [], environment: '', loading: false });
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to add connector');
        }
    };

    const setEntryForm = (envId: number, patch: Partial<{ name: string; value: string; kind: 'secret' | 'variable' }>) =>
        setEntryForms((s) => {
            const prev = s[envId] ?? { name: '', value: '', kind: 'secret' as const };
            return { ...s, [envId]: { ...prev, ...patch } };
        });

    const entriesOf = (env: ProjectEnv, kind: 'secret' | 'variable') => env.secrets.filter((s) => s.kind === kind);

    // Remote-only entries: present on a connected target but missing locally.
    // Only the NAME is known (secret values are write-only on the targets) —
    // shown so the target state is visible, and updatable: setting a value
    // stores it here and overwrites the target on push.
    const remoteOnlyOf = (env: ProjectEnv, kind: 'secret' | 'variable') => {
        const local = new Set(env.secrets.map((s) => s.name));
        return (remote[env.id]?.entries || []).filter((r) => r.kind === kind && !local.has(r.name));
    };

    const renderEntryList = (env: ProjectEnv, kind: 'secret' | 'variable') => {
        const list = entriesOf(env, kind);
        const remoteOnly = remoteOnlyOf(env, kind);
        return (
            <div className="flex-1" data-testid={`${kind}s-${env.name}`}>
                <div className="text-xs font-semibold uppercase tracking-wide text-gray-500">
                    {kind === 'secret' ? 'Secrets' : 'Variables'}
                </div>
                <p className="text-xs text-gray-400">
                    {kind === 'secret'
                        ? 'Write-only on targets (GitHub env secrets / Vercel sensitive). Redacted from all logs.'
                        : 'Readable on targets (GitHub env variables / Vercel plain). Encrypted at rest here.'}
                </p>
                {list.length === 0 && remoteOnly.length === 0 ? (
                    <p className="mt-1 text-sm text-gray-400">None yet.</p>
                ) : (
                    <ul className="mt-1 divide-y">
                        {list.map((s) => (
                            <li key={s.id} className="flex items-center justify-between py-1 text-sm" data-testid={`entry-${s.name}`}>
                                <span className="flex items-center gap-2 font-mono text-gray-800">
                                    {kind === 'secret' && <Lock size={12} className="text-emerald-600" />}
                                    {s.name}
                                    {kind === 'secret' && <span className="font-sans text-xs text-gray-400">••••••••</span>}
                                </span>
                                <button onClick={() => deleteEntry(env, s.name)} className="text-gray-400 hover:text-red-600" title={`Delete ${s.name}`}>
                                    <Trash2 size={13} />
                                </button>
                            </li>
                        ))}
                        {remoteOnly.map((r) => (
                            <li key={'remote-' + r.name} className="flex items-center justify-between py-1 text-sm" data-testid={`remote-entry-${r.name}`}>
                                <span className="flex items-center gap-2 font-mono text-gray-500">
                                    {kind === 'secret' && <Lock size={12} className="text-gray-400" />}
                                    {r.name}
                                    <span
                                        className="rounded bg-amber-50 px-1.5 py-0.5 font-sans text-xs text-amber-700"
                                        title="Exists on the connected target but not here. The value cannot be read back — set a new value to manage it from here."
                                    >
                                        on target only
                                    </span>
                                </span>
                                <button
                                    onClick={() => setEntryForm(env.id, { name: r.name, kind: r.kind })}
                                    className="text-xs text-indigo-600 hover:text-indigo-800"
                                    title={`Set a value for ${r.name} (overwrites the target on push)`}
                                >
                                    Set value
                                </button>
                            </li>
                        ))}
                    </ul>
                )}
            </div>
        );
    };

    return (
        <div className="bg-white rounded-lg shadow p-6 mt-6" data-testid="project-environments">
            <h2 className="text-lg font-semibold text-gray-800">Environments</h2>
            <p className="mt-1 text-sm text-gray-600">
                Deploy environments hold this project's secrets and variables (all encrypted with your
                passkey). Connect one to Vercel or GitHub and every change is pushed there, so the next
                deployment has the current values.
            </p>
            {error && <div className="mt-2 rounded bg-red-50 p-2 text-sm text-red-700">{error}</div>}

            <div className="mt-3 flex gap-2">
                <input
                    value={newEnvName}
                    onChange={(e) => setNewEnvName(e.target.value)}
                    onKeyDown={(e) => e.key === 'Enter' && createEnv()}
                    placeholder="New environment (e.g. production)"
                    className="w-64 rounded border p-2 text-sm focus:border-indigo-500 focus:outline-none"
                />
                <button onClick={createEnv} className="flex items-center gap-1 rounded bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-700">
                    <Plus size={15} /> Add environment
                </button>
            </div>

            <div className="mt-4 space-y-5">
                {envs.map((env) => (
                    <div key={env.id} className="rounded border p-4" data-testid={`project-env-${env.name}`}>
                        <div className="flex items-center justify-between">
                            <h3 className="font-semibold text-gray-800">{env.name}</h3>
                            <div className="flex items-center gap-2">
                                {(env.connectors?.length ?? 0) > 0 && (
                                    <button
                                        onClick={() => syncNow(env)}
                                        disabled={syncing === env.id}
                                        className="flex items-center gap-1 rounded border px-2 py-1 text-xs text-gray-600 hover:bg-gray-50 disabled:opacity-50"
                                        title="Push all entries to connected targets now"
                                    >
                                        <RefreshCw size={12} className={syncing === env.id ? 'animate-spin' : ''} /> Sync now
                                    </button>
                                )}
                                <button onClick={() => deleteEnv(env)} className="text-gray-400 hover:text-red-600" title="Delete environment">
                                    <Trash2 size={15} />
                                </button>
                            </div>
                        </div>

                        <div className="mt-3 flex gap-6">
                            {renderEntryList(env, 'variable')}
                            {renderEntryList(env, 'secret')}
                        </div>

                        {/* Add entry form */}
                        <div className="mt-3 flex items-end gap-2 border-t pt-3">
                            <div>
                                <label className="mb-1 block text-xs text-gray-500">Type</label>
                                <select
                                    value={entryForms[env.id]?.kind || 'secret'}
                                    onChange={(e) => setEntryForm(env.id, { kind: e.target.value as 'secret' | 'variable' })}
                                    className="rounded border p-2 text-sm"
                                >
                                    <option value="secret">Secret</option>
                                    <option value="variable">Variable</option>
                                </select>
                            </div>
                            <div className="w-48">
                                <label className="mb-1 block text-xs text-gray-500">Name</label>
                                <input
                                    value={entryForms[env.id]?.name || ''}
                                    onChange={(e) => setEntryForm(env.id, { name: e.target.value.toUpperCase() })}
                                    placeholder="API_KEY"
                                    className="w-full rounded border p-2 font-mono text-sm focus:border-indigo-500 focus:outline-none"
                                />
                            </div>
                            <div className="flex-1">
                                <SecretLabel>Value</SecretLabel>
                                <SecretField
                                    value={entryForms[env.id]?.value || ''}
                                    onChange={(v) => setEntryForm(env.id, { value: v })}
                                    placeholder="value"
                                />
                            </div>
                            <button
                                onClick={() => saveEntry(env)}
                                disabled={!entryForms[env.id]?.name || !entryForms[env.id]?.value}
                                className="rounded bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-40"
                            >
                                Save
                            </button>
                        </div>

                        {/* Connectors */}
                        <div className="mt-4 border-t pt-3">
                            <div className="flex items-center justify-between">
                                <div className="text-xs font-semibold uppercase tracking-wide text-gray-500">Deploy targets</div>
                                <button
                                    onClick={() => { setWizardEnv(wizardEnv === env.id ? null : env.id); }}
                                    className="flex items-center gap-1 rounded border px-2 py-1 text-xs text-gray-600 hover:bg-gray-50"
                                >
                                    <Link2 size={12} /> Connect
                                </button>
                            </div>

                            {(remote[env.id]?.errors?.length ?? 0) > 0 && (
                                <p className="mt-1 text-xs text-red-600">
                                    Could not read target state: {remote[env.id]!.errors![0].provider} — {remote[env.id]!.errors![0].error}
                                </p>
                            )}
                            {(env.connectors?.length ?? 0) === 0 ? (
                                <p className="mt-1 text-sm text-gray-400">Not connected — secrets are only stored here.</p>
                            ) : (
                                <ul className="mt-2 space-y-1">
                                    {env.connectors!.map((c) => (
                                        <li key={c.id} className="flex items-center justify-between rounded bg-gray-50 px-2 py-1.5 text-sm" data-testid={`connector-${c.provider}-${c.target_project}`}>
                                            <span className="flex items-center gap-2">
                                                <span className="font-medium capitalize">{c.provider}</span>
                                                <span className="text-gray-600">{c.target_project}</span>
                                                <span className="rounded bg-gray-200 px-1.5 py-0.5 text-xs text-gray-600">{c.target_environment}</span>
                                                {c.last_sync_status === 'ok' && (
                                                    <span className="flex items-center gap-1 text-xs text-emerald-700" title={c.last_sync_at || ''}>
                                                        <CheckCircle2 size={12} /> synced
                                                    </span>
                                                )}
                                                {c.last_sync_status === 'error' && (
                                                    <span className="flex items-center gap-1 text-xs text-red-600" title={c.last_sync_error}>
                                                        <AlertCircle size={12} /> sync error
                                                    </span>
                                                )}
                                            </span>
                                            <button onClick={() => deleteConnector(c.id)} className="text-gray-400 hover:text-red-600" title="Disconnect">
                                                <Trash2 size={13} />
                                            </button>
                                        </li>
                                    ))}
                                </ul>
                            )}

                            {/* Connector wizard */}
                            {wizardEnv === env.id && (
                                <div className="mt-3 rounded border border-dashed p-3" data-testid="connector-wizard">
                                    <div className="flex items-end gap-2">
                                        <div>
                                            <label className="mb-1 block text-xs text-gray-500">Provider</label>
                                            <select
                                                value={wizard.provider}
                                                onChange={(e) => setWizard((s) => ({ ...s, provider: e.target.value as 'vercel' | 'github', projects: [], project: '', environments: [], environment: '' }))}
                                                className="rounded border p-2 text-sm"
                                            >
                                                <option value="vercel">Vercel</option>
                                                <option value="github">GitHub</option>
                                            </select>
                                        </div>
                                        {wizard.provider === 'vercel' && (
                                            <div className="w-40">
                                                <label className="mb-1 block text-xs text-gray-500">Team ID (optional)</label>
                                                <input
                                                    value={wizard.teamId}
                                                    onChange={(e) => setWizard((s) => ({ ...s, teamId: e.target.value }))}
                                                    placeholder="team_…"
                                                    className="w-full rounded border p-2 text-sm"
                                                />
                                            </div>
                                        )}
                                        <div className="flex-1">
                                            <SecretLabel>{wizard.provider === 'vercel' ? 'Vercel access token' : 'GitHub token (repo/environments scope)'}</SecretLabel>
                                            <SecretField
                                                value={wizard.token}
                                                onChange={(v) => setWizard((s) => ({ ...s, token: v }))}
                                                placeholder="token"
                                            />
                                        </div>
                                        <button
                                            onClick={discoverProjects}
                                            disabled={!wizard.token || wizard.loading}
                                            className="rounded bg-gray-700 px-3 py-2 text-sm font-medium text-white hover:bg-gray-800 disabled:opacity-40"
                                        >
                                            {wizard.loading ? 'Loading…' : wizard.provider === 'vercel' ? 'List projects' : 'List repos'}
                                        </button>
                                    </div>

                                    {wizard.projects.length > 0 && (
                                        <div className="mt-2 flex items-end gap-2">
                                            <div className="flex-1">
                                                <label className="mb-1 block text-xs text-gray-500">{wizard.provider === 'vercel' ? 'Project' : 'Repository'}</label>
                                                <select
                                                    value={wizard.project}
                                                    onChange={(e) => discoverEnvironments(e.target.value)}
                                                    className="w-full rounded border p-2 text-sm"
                                                >
                                                    <option value="">Select…</option>
                                                    {wizard.projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                                                </select>
                                            </div>
                                            <div className="flex-1">
                                                <label className="mb-1 block text-xs text-gray-500">Environment</label>
                                                <select
                                                    value={wizard.environment}
                                                    onChange={(e) => setWizard((s) => ({ ...s, environment: e.target.value }))}
                                                    disabled={wizard.environments.length === 0}
                                                    className="w-full rounded border p-2 text-sm disabled:bg-gray-50"
                                                >
                                                    <option value="">Select…</option>
                                                    {wizard.environments.map((en) => <option key={en.id} value={en.id}>{en.name}</option>)}
                                                </select>
                                            </div>
                                            <button
                                                onClick={() => createConnector(env)}
                                                disabled={!wizard.project || !wizard.environment}
                                                className="rounded bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-40"
                                            >
                                                Connect
                                            </button>
                                        </div>
                                    )}
                                </div>
                            )}
                        </div>
                    </div>
                ))}
            </div>
        </div>
    );
}
