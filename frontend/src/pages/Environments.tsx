import { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import { Trash2, Pencil, Check, X, Lock, Server } from 'lucide-react';
import { useStore } from '../store';
import { SecretField, SecretLabel } from '../components/SecretField';

interface EnvironmentSecret {
    id: number;
    environment_id: number;
    name: string;
    has_value: boolean;
}

interface SystemSecret {
    kind: string;
    name: string;
    has_value: boolean;
}

interface Environment {
    id: number;
    company_id: number;
    name: string;
    is_default: boolean;
    builtin: boolean;
    secrets: EnvironmentSecret[];
    system_secrets?: SystemSecret[];
}

// Environments — named secret sets a task's runs receive as shell env vars.
// The agent can use a secret by name ($API_KEY) but never sees the value:
// values are sealed with the user's passkey-unlocked key at rest and redacted
// from all run output. The default environment additionally shows the
// headcount1-managed credentials (provider keys, MCP tokens, SSH key) as a
// separate read-only group: those are used server-side and never enter the
// agent's shell.
export function Environments() {
    const { selectedCompanyId } = useStore();
    const [envs, setEnvs] = useState<Environment[]>([]);
    const [error, setError] = useState('');
    const [renamingId, setRenamingId] = useState<number | null>(null);
    const [renameValue, setRenameValue] = useState('');
    // Per-env new secret form state.
    const [secretForms, setSecretForms] = useState<Record<number, { name: string; value: string }>>({});

    const load = useCallback(async () => {
        if (!selectedCompanyId) return;
        try {
            const res = await axios.get(`/api/companies/${selectedCompanyId}/environments`);
            setEnvs(res.data || []);
            setError('');
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to load environments');
        }
    }, [selectedCompanyId]);

    useEffect(() => { load(); }, [load]);

    const renameEnv = async (id: number) => {
        try {
            await axios.put(`/api/environments/${id}`, { name: renameValue });
            setRenamingId(null);
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to rename environment');
        }
    };

    const deleteEnv = async (env: Environment) => {
        if (!confirm(`Delete environment "${env.name}" and its secrets? Tasks using it fall back to the default environment.`)) return;
        try {
            await axios.delete(`/api/environments/${env.id}`);
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to delete environment');
        }
    };

    const saveSecret = async (env: Environment) => {
        const form = secretForms[env.id];
        if (!form?.name?.trim() || !form?.value) return;
        try {
            await axios.put(`/api/environments/${env.id}/secrets`, { name: form.name.trim(), value: form.value });
            setSecretForms((s) => ({ ...s, [env.id]: { name: '', value: '' } }));
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to save secret');
        }
    };

    const deleteSecret = async (env: Environment, name: string) => {
        if (!confirm(`Delete secret ${name} from "${env.name}"?`)) return;
        try {
            await axios.delete(`/api/environments/${env.id}/secrets/${encodeURIComponent(name)}`);
            await load();
        } catch (e: any) {
            setError(e?.response?.data?.error || 'Failed to delete secret');
        }
    };

    const setForm = (envId: number, patch: Partial<{ name: string; value: string }>) =>
        setSecretForms((s) => {
            const prev = s[envId] ?? { name: '', value: '' };
            return { ...s, [envId]: { ...prev, ...patch } };
        });

    return (
        <div className="p-6 max-w-4xl">
            <h1 className="text-2xl font-bold text-gray-900">Environments</h1>
            <p className="mt-1 text-sm text-gray-600">
                Tasks always run in <strong>headcount1 cloud</strong>: its secrets are injected into the
                agent's shell as env vars. Agents can <em>use</em> a secret (<code>$API_KEY</code>) but
                never <em>see</em> it — values are encrypted with your passkey and redacted from all run
                output and logs. Deploy environments (with Vercel/GitHub sync) live in each project's
                settings.
            </p>
            {error && <div className="mt-3 rounded bg-red-50 p-2 text-sm text-red-700">{error}</div>}

            <div className="mt-6 space-y-6">
                {envs.map((env) => (
                    <div key={env.id} className="rounded-lg border bg-white p-4 shadow-sm" data-testid={`env-${env.name}`}>
                        <div className="flex items-center justify-between">
                            <div className="flex items-center gap-2">
                                {renamingId === env.id ? (
                                    <span className="flex items-center gap-1">
                                        <input
                                            value={renameValue}
                                            onChange={(e) => setRenameValue(e.target.value)}
                                            className="rounded border p-1 text-sm"
                                            autoFocus
                                        />
                                        <button onClick={() => renameEnv(env.id)} className="text-emerald-600 hover:text-emerald-800"><Check size={16} /></button>
                                        <button onClick={() => setRenamingId(null)} className="text-gray-400 hover:text-gray-600"><X size={16} /></button>
                                    </span>
                                ) : (
                                    <h2 className="text-lg font-semibold text-gray-800">{env.name}</h2>
                                )}
                                {env.is_default && (
                                    <span className="rounded bg-indigo-100 px-2 py-0.5 text-xs font-medium text-indigo-700">default</span>
                                )}
                                {env.builtin && (
                                    <span className="rounded bg-gray-100 px-2 py-0.5 text-xs text-gray-500">built-in</span>
                                )}
                            </div>
                            {!env.builtin && renamingId !== env.id && (
                                <div className="flex items-center gap-2">
                                    <button
                                        onClick={() => { setRenamingId(env.id); setRenameValue(env.name); }}
                                        className="text-gray-400 hover:text-gray-600"
                                        title="Rename"
                                    >
                                        <Pencil size={16} />
                                    </button>
                                    <button onClick={() => deleteEnv(env)} className="text-gray-400 hover:text-red-600" title="Delete">
                                        <Trash2 size={16} />
                                    </button>
                                </div>
                            )}
                        </div>

                        {/* System-managed credentials: shown only in the default env, read-only. */}
                        {env.is_default && (env.system_secrets?.length ?? 0) > 0 && (
                            <div className="mt-3 rounded border border-dashed border-gray-200 bg-gray-50 p-3">
                                <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-gray-500">
                                    <Server size={13} /> Managed by headcount1
                                </div>
                                <p className="mt-1 text-xs text-gray-500">
                                    Used by the platform itself (LLM calls, MCP servers, git) — never exposed to the
                                    agent's shell. Manage them on their own pages.
                                </p>
                                <ul className="mt-2 space-y-1">
                                    {env.system_secrets!.map((s) => (
                                        <li key={s.kind + s.name} className="flex items-center gap-2 text-sm text-gray-700">
                                            <Lock size={13} className="text-gray-400" />
                                            <span>{s.name}</span>
                                            <span className="text-xs text-emerald-700">set</span>
                                        </li>
                                    ))}
                                </ul>
                            </div>
                        )}

                        <div className="mt-3">
                            <div className="text-xs font-semibold uppercase tracking-wide text-gray-500">Secrets</div>
                            {env.secrets.length === 0 ? (
                                <p className="mt-1 text-sm text-gray-400">No secrets yet.</p>
                            ) : (
                                <ul className="mt-2 divide-y">
                                    {env.secrets.map((s) => (
                                        <li key={s.id} className="flex items-center justify-between py-1.5 text-sm" data-testid={`secret-${s.name}`}>
                                            <span className="flex items-center gap-2 font-mono text-gray-800">
                                                <Lock size={13} className="text-emerald-600" />
                                                {s.name}
                                                <span className="font-sans text-xs text-gray-400">••••••••</span>
                                            </span>
                                            <button
                                                onClick={() => deleteSecret(env, s.name)}
                                                className="text-gray-400 hover:text-red-600"
                                                title="Delete secret"
                                            >
                                                <Trash2 size={14} />
                                            </button>
                                        </li>
                                    ))}
                                </ul>
                            )}

                            <div className="mt-3 flex items-end gap-2">
                                <div className="w-56">
                                    <label className="mb-1 block text-xs text-gray-500">Name</label>
                                    <input
                                        value={secretForms[env.id]?.name || ''}
                                        onChange={(e) => setForm(env.id, { name: e.target.value.toUpperCase() })}
                                        placeholder="API_KEY"
                                        className="w-full rounded border p-2 font-mono text-sm focus:border-indigo-500 focus:outline-none"
                                    />
                                </div>
                                <div className="flex-1">
                                    <SecretLabel>Value</SecretLabel>
                                    <SecretField
                                        value={secretForms[env.id]?.value || ''}
                                        onChange={(v) => setForm(env.id, { value: v })}
                                        placeholder="secret value"
                                    />
                                </div>
                                <button
                                    onClick={() => saveSecret(env)}
                                    disabled={!secretForms[env.id]?.name || !secretForms[env.id]?.value}
                                    className="rounded bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-40"
                                >
                                    Save
                                </button>
                            </div>
                        </div>
                    </div>
                ))}
            </div>
        </div>
    );
}
