import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { Plus, Trash2, Edit2, Minus, Copy, Check, ArrowUp, ArrowDown, BarChart3, X } from 'lucide-react';

interface MemberWindow {
    requests: number;
    failures: number;
    rate_limited: number;
    failure_rate: number;
    avg_tokens_per_sec: number;
}

interface MemberStat {
    provider_id: number;
    provider_name: string;
    model: string;
    all_models: boolean;
    is_free: boolean;
    window_5h: MemberWindow;
    window_3d: MemberWindow;
    rate_limited_now: boolean;
    cooldown_until: string | null;
}

interface StatsBucket {
    start: string;
    success: number;
    failures: number;
    rate_limited: number;
}

interface GroupStats {
    members: MemberStat[];
    buckets: StatsBucket[];
}

interface MemberForm {
    provider_id: number | '';
    model: string;
    all_models: boolean;
    is_free: boolean;
}

// Sentinel <select> value for "any model from this provider".
const ANY_MODEL = '__any__';

// Status colors (state encoding): success / failure / rate-limited.
const STATUS = {
    success: { color: '#0ca30c', label: 'Success' },
    failure: { color: '#ec835a', label: 'Failed' },
    rateLimited: { color: '#fab219', label: 'Rate limited' },
};

const looksFree = (model: string) => model.toLowerCase().includes('free');

// Hourly stacked bars over the last 3 days: success / failed / rate-limited.
const RequestsChart: React.FC<{ buckets: StatsBucket[] }> = ({ buckets }) => {
    if (!buckets || buckets.length === 0) return null;
    const total = buckets.reduce((s, b) => s + b.success + b.failures + b.rate_limited, 0);
    if (total === 0) {
        return <p className="text-xs text-gray-400 mt-3">No requests recorded in the last 3 days.</p>;
    }
    const H = 64;
    const max = Math.max(...buckets.map(b => b.success + b.failures + b.rate_limited), 1);
    const barW = 100 / buckets.length;
    return (
        <div className="mt-3">
            <svg viewBox={`0 0 100 ${H}`} preserveAspectRatio="none" className="w-full" style={{ height: H }} role="img" aria-label="Requests per hour by outcome, last 3 days">
                {buckets.map((b, i) => {
                    const segs = [
                        { v: b.success, c: STATUS.success.color },
                        { v: b.rate_limited, c: STATUS.rateLimited.color },
                        { v: b.failures, c: STATUS.failure.color },
                    ];
                    let y = H;
                    const x = i * barW;
                    const title = `${new Date(b.start).toLocaleString()} — ${b.success} ok, ${b.failures} failed, ${b.rate_limited} rate-limited`;
                    return (
                        <g key={i}>
                            <title>{title}</title>
                            {segs.map((s, j) => {
                                if (s.v === 0) return null;
                                const h = (s.v / max) * (H - 4);
                                y -= h;
                                // 2px-equivalent gap between stacked segments and adjacent bars
                                return <rect key={j} x={x + barW * 0.15} y={y + 0.75} width={barW * 0.7} height={Math.max(h - 1.5, 0.75)} rx={0.8} fill={s.c} />;
                            })}
                        </g>
                    );
                })}
            </svg>
            <div className="flex items-center gap-4 mt-1.5">
                {Object.values(STATUS).map(s => (
                    <span key={s.label} className="flex items-center gap-1.5 text-xs text-gray-600">
                        <span className="inline-block w-2.5 h-2.5 rounded-sm" style={{ backgroundColor: s.color }} />
                        {s.label}
                    </span>
                ))}
                <span className="text-xs text-gray-400 ml-auto">last 3 days, hourly</span>
            </div>
        </div>
    );
};

const pct = (rate: number) => `${(rate * 100).toFixed(rate > 0 && rate < 0.1 ? 1 : 0)}%`;

const WindowCell: React.FC<{ w: MemberWindow }> = ({ w }) => {
    if (!w || w.requests === 0) return <span className="text-gray-300">—</span>;
    const bad = w.failure_rate >= 0.3;
    return (
        <span className={bad ? 'text-red-600 font-medium' : 'text-gray-700'}>
            {pct(w.failure_rate)}
            <span className="text-gray-400 font-normal"> / {w.requests} req</span>
        </span>
    );
};

export const ModelGroups: React.FC<{ providers: any[]; onChange?: () => void }> = ({ providers, onChange }) => {
    const [groups, setGroups] = useState<any[]>([]);
    const [stats, setStats] = useState<Record<number, GroupStats>>({});
    const [isModalOpen, setIsModalOpen] = useState(false);
    const [editingId, setEditingId] = useState<number | null>(null);
    const [formName, setFormName] = useState('');
    const [formDescription, setFormDescription] = useState('');
    const [formMembers, setFormMembers] = useState<MemberForm[]>([]);
    const [saveError, setSaveError] = useState('');
    const [isSaving, setIsSaving] = useState(false);
    const [copiedId, setCopiedId] = useState<number | null>(null);
    const [statsGroupId, setStatsGroupId] = useState<number | null>(null);

    const fetchGroups = useCallback(async () => {
        try {
            const res = await axios.get('/api/model-groups');
            const gs = res.data || [];
            setGroups(gs);
            const entries = await Promise.all(gs.map(async (g: any) => {
                try {
                    const s = await axios.get(`/api/model-groups/${g.id}/stats`);
                    return [g.id, s.data] as const;
                } catch {
                    return [g.id, null] as const;
                }
            }));
            setStats(Object.fromEntries(entries.filter(([, v]) => v)));
        } catch (e) {
            console.error(e);
        }
    }, []);

    useEffect(() => {
        fetchGroups();
    }, [fetchGroups]);

    const groupUrl = (slug: string) => `${window.location.origin}/api/proxy/group/${slug}/v1`;

    const copyUrl = (g: any) => {
        navigator.clipboard.writeText(groupUrl(g.slug));
        setCopiedId(g.id);
        setTimeout(() => setCopiedId(null), 1500);
    };

    const openModal = (g?: any) => {
        setSaveError('');
        if (g) {
            setEditingId(g.id);
            setFormName(g.name);
            setFormDescription(g.description || '');
            setFormMembers((g.members || []).map((m: any) => ({
                provider_id: m.provider_id, model: m.model, all_models: !!m.all_models, is_free: m.is_free,
            })));
        } else {
            setEditingId(null);
            setFormName('');
            setFormDescription('');
            setFormMembers([{ provider_id: '', model: '', all_models: false, is_free: false }]);
        }
        setIsModalOpen(true);
    };

    const handleSave = async () => {
        setIsSaving(true);
        setSaveError('');
        try {
            const members = formMembers
                .filter(m => m.provider_id && (m.all_models || m.model.trim()))
                .map(m => ({
                    provider_id: m.provider_id,
                    model: m.all_models ? '' : m.model.trim(),
                    all_models: m.all_models,
                    is_free: m.is_free,
                }));
            const payload = { name: formName, description: formDescription, members };
            if (editingId) {
                await axios.put(`/api/model-groups/${editingId}`, payload);
            } else {
                await axios.post('/api/model-groups', payload);
            }
            setIsModalOpen(false);
            fetchGroups();
            onChange?.();
        } catch (e: any) {
            setSaveError(e.response?.data?.error || 'Save failed');
        } finally {
            setIsSaving(false);
        }
    };

    const handleDelete = async (g: any) => {
        if (!window.confirm('Delete this model group?')) return;
        try {
            await axios.delete(`/api/model-groups/${g.id}`);
            fetchGroups();
            onChange?.();
        } catch (e) {
            console.error(e);
        }
    };

    const updateMember = (idx: number, patch: Partial<MemberForm>) => {
        setFormMembers(ms => ms.map((m, i) => (i === idx ? { ...m, ...patch } : m)));
    };

    const moveMember = (idx: number, dir: -1 | 1) => {
        setFormMembers(ms => {
            const next = [...ms];
            const j = idx + dir;
            if (j < 0 || j >= next.length) return ms;
            [next[idx], next[j]] = [next[j], next[idx]];
            return next;
        });
    };

    const providerModels = (providerId: number | ''): string[] => {
        const p = providers.find(pr => pr.id === providerId);
        if (!p?.supported_models) return [];
        return p.supported_models.split(',').map((m: string) => m.trim()).filter(Boolean);
    };

    // Options for a member's model dropdown: the provider's live catalog,
    // plus the member's already-saved model if the catalog doesn't (yet, or
    // anymore) list it — otherwise a stale/pending catalog would make the
    // dropdown show blank and silently wipe a valid saved model on save.
    const modelOptionsFor = (m: MemberForm): string[] => {
        const live = providerModels(m.provider_id);
        if (m.model && !live.includes(m.model)) return [m.model, ...live];
        return live;
    };

    const memberStatFor = (groupId: number, m: any): MemberStat | undefined =>
        stats[groupId]?.members?.find(s => s.provider_id === m.provider_id &&
            (m.all_models ? s.all_models : s.model === m.model));

    // Compact card summary over the last 3 days: total requests, the share
    // that needed a fallback (failed or rate-limited attempts — every one
    // of those means the router moved on to another model), a
    // request-weighted average tokens/sec across members, and — only when
    // the group actually mixes free and paid members, where the split is
    // meaningful — the share of requests served by a free model.
    const summaryFor = (g: any) => {
        const members = stats[g.id]?.members;
        if (!members || members.length === 0) return null;
        let requests = 0, fallbacks = 0, tpsSum = 0, tpsWeight = 0, freeRequests = 0;
        for (const m of members) {
            const w = m.window_3d;
            requests += w.requests;
            fallbacks += w.failures + w.rate_limited;
            if (w.avg_tokens_per_sec > 0) {
                tpsSum += w.avg_tokens_per_sec * w.requests;
                tpsWeight += w.requests;
            }
            if (m.is_free) freeRequests += w.requests;
        }
        if (requests === 0) return { requests: 0 };
        const hasFree = (g.members || []).some((m: any) => m.is_free);
        const hasPaid = (g.members || []).some((m: any) => !m.is_free);
        return {
            requests,
            fallbackRate: fallbacks / requests,
            avgTokensPerSec: tpsWeight > 0 ? tpsSum / tpsWeight : 0,
            freeSharePct: (hasFree && hasPaid) ? freeRequests / requests : null,
        };
    };

    return (
        <div className="space-y-4">
            <div className="flex justify-between items-center">
                <div>
                    <h2 className="text-xl font-bold">Model Groups</h2>
                    <p className="text-sm text-gray-500">
                        Each group is an OpenAI-compatible endpoint that routes to the healthiest model — free models first, with automatic retries and failover on errors or rate limits.
                    </p>
                </div>
                <button onClick={() => openModal()} className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700 shadow-sm shrink-0">
                    <Plus size={16} className="mr-2" /> Add Group
                </button>
            </div>

            {groups.length === 0 && (
                <p className="text-sm text-gray-400">No model groups yet.</p>
            )}

            <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
                {groups.map(g => (
                    <div key={g.id} className="bg-white p-6 rounded-lg border shadow-sm">
                        <div className="flex justify-between items-start mb-2">
                            <h3 className="text-lg font-bold text-gray-900 flex items-center gap-2">
                                {g.name}
                            </h3>
                            <div className="flex space-x-2">
                                <button onClick={() => setStatsGroupId(g.id)} className="text-gray-500 hover:text-indigo-600" title="View stats"><BarChart3 size={18} /></button>
                                <button onClick={() => openModal(g)} className="text-gray-500 hover:text-gray-700" title="Edit group"><Edit2 size={18} /></button>
                                <button onClick={() => handleDelete(g)} className="text-red-500 hover:text-red-700" title="Delete group"><Trash2 size={18} /></button>
                            </div>
                        </div>
                        {g.description && <p className="text-sm text-gray-600 mb-2">{g.description}</p>}

                        <div className="flex items-center gap-2 mb-3 bg-gray-50 border rounded px-2 py-1.5">
                            <code className="text-xs text-gray-700 truncate flex-1">{groupUrl(g.slug)}</code>
                            <button onClick={() => copyUrl(g)} className="text-gray-500 hover:text-indigo-600 shrink-0" title="Copy endpoint URL">
                                {copiedId === g.id ? <Check size={15} className="text-green-600" /> : <Copy size={15} />}
                            </button>
                        </div>

                        <p className="text-xs text-gray-500">
                            {(g.members || []).length} model{(g.members || []).length === 1 ? '' : 's'} configured
                            {(() => {
                                const s = summaryFor(g);
                                if (!s || !s.requests) return ' · no requests in the last 3 days';
                                return (
                                    <>
                                        {' · '}{s.requests} request{s.requests === 1 ? '' : 's'} (3d)
                                        {' · '}<span className={s.fallbackRate! >= 0.3 ? 'text-red-600 font-medium' : ''}>{pct(s.fallbackRate!)} fallback</span>
                                        {s.avgTokensPerSec! > 0 && <>{' · '}{s.avgTokensPerSec!.toFixed(1)} tok/s avg</>}
                                        {s.freeSharePct !== null && <>{' · '}{pct(s.freeSharePct!)} free</>}
                                    </>
                                );
                            })()}
                        </p>
                    </div>
                ))}
            </div>

            {isModalOpen && (
                <div className="fixed inset-0 bg-black/50 flex justify-center items-center z-50">
                    <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-3xl max-h-[90vh] flex flex-col">
                        <h2 className="text-xl font-bold mb-4">{editingId ? 'Edit Model Group' : 'Add Model Group'}</h2>
                        <div className="space-y-4 overflow-y-auto flex-1 pr-2">
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Name</label>
                                <input required type="text" value={formName} onChange={e => setFormName(e.target.value)} placeholder="e.g. memory management" className="w-full border rounded p-2" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
                                <input type="text" value={formDescription} onChange={e => setFormDescription(e.target.value)} className="w-full border rounded p-2" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-2">Models (tried top to bottom; free models always go first)</label>
                                {formMembers.map((m, idx) => (
                                    <div key={idx} className="flex items-center gap-2 mb-2">
                                        <select
                                            value={m.provider_id}
                                            onChange={e => updateMember(idx, { provider_id: e.target.value ? Number(e.target.value) : '', model: '', all_models: false })}
                                            className="border rounded p-2 text-sm w-56 shrink-0"
                                        >
                                            <option value="">Provider…</option>
                                            {providers.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                                        </select>
                                        <select
                                            value={m.all_models ? ANY_MODEL : m.model}
                                            disabled={!m.provider_id}
                                            onChange={e => {
                                                const v = e.target.value;
                                                if (v === ANY_MODEL) {
                                                    updateMember(idx, { model: '', all_models: true });
                                                } else {
                                                    updateMember(idx, { model: v, all_models: false, is_free: looksFree(v) });
                                                }
                                            }}
                                            className="flex-1 min-w-0 border rounded p-2 text-sm disabled:bg-gray-50 disabled:text-gray-400"
                                        >
                                            <option value="">Model…</option>
                                            <option value={ANY_MODEL}>Any model (all currently supported by this provider)</option>
                                            {modelOptionsFor(m).map(pm => <option key={pm} value={pm}>{pm}</option>)}
                                        </select>
                                        <label className="flex items-center gap-1 text-xs text-gray-600 shrink-0">
                                            <input type="checkbox" checked={m.is_free} onChange={e => updateMember(idx, { is_free: e.target.checked })} />
                                            free
                                        </label>
                                        <button type="button" onClick={() => moveMember(idx, -1)} disabled={idx === 0} className="text-gray-400 hover:text-gray-700 disabled:opacity-30 shrink-0"><ArrowUp size={15} /></button>
                                        <button type="button" onClick={() => moveMember(idx, 1)} disabled={idx === formMembers.length - 1} className="text-gray-400 hover:text-gray-700 disabled:opacity-30 shrink-0"><ArrowDown size={15} /></button>
                                        <button type="button" onClick={() => setFormMembers(ms => ms.filter((_, i) => i !== idx))} className="text-red-500 hover:text-red-700 shrink-0"><Minus size={16} /></button>
                                    </div>
                                ))}
                                <button type="button" onClick={() => setFormMembers(ms => [...ms, { provider_id: '', model: '', all_models: false, is_free: false }])} className="text-indigo-600 hover:text-indigo-800 text-sm font-medium flex items-center mt-2">
                                    <Plus size={14} className="mr-1" /> Add Model
                                </button>
                            </div>
                            {saveError && (
                                <div className="p-3 rounded text-sm bg-red-50 text-red-800 border border-red-200">{saveError}</div>
                            )}
                        </div>
                        <div className="flex justify-end space-x-3 pt-4 border-t mt-4">
                            <button type="button" onClick={() => setIsModalOpen(false)} className="text-gray-500 hover:text-gray-700 px-4 py-2">Cancel</button>
                            <button type="button" onClick={handleSave} disabled={isSaving || !formName.trim()} className={`bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 ${isSaving || !formName.trim() ? 'opacity-50 cursor-not-allowed' : ''}`}>
                                {isSaving ? 'Saving...' : 'Save Group'}
                            </button>
                        </div>
                    </div>
                </div>
            )}

            {statsGroupId !== null && (() => {
                const g = groups.find(gr => gr.id === statsGroupId);
                if (!g) return null;
                return (
                    <div className="fixed inset-0 bg-black/50 flex justify-center items-center z-50">
                        <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-2xl max-h-[90vh] flex flex-col">
                            <div className="flex justify-between items-start mb-4">
                                <h2 className="text-xl font-bold">{g.name} — Stats</h2>
                                <button onClick={() => setStatsGroupId(null)} className="text-gray-400 hover:text-gray-700"><X size={20} /></button>
                            </div>
                            <div className="overflow-y-auto flex-1 pr-1">
                                <table className="w-full text-xs">
                                    <thead>
                                        <tr className="text-left text-gray-400">
                                            <th className="font-medium py-1">Model</th>
                                            <th className="font-medium py-1">Fail % (5h)</th>
                                            <th className="font-medium py-1">Fail % (3d)</th>
                                            <th className="font-medium py-1 text-right">tok/s</th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {(g.members || []).map((m: any) => {
                                            const s = memberStatFor(g.id, m);
                                            return (
                                                <tr key={m.id} className="border-t">
                                                    <td className="py-1.5 pr-2">
                                                        <div className="flex items-center gap-1.5 flex-wrap">
                                                            <span className="text-gray-800 break-all">{m.all_models ? 'Any model' : m.model}</span>
                                                            <span className={`px-1.5 py-0.5 rounded-full text-[10px] font-medium ${m.is_free ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-600'}`}>
                                                                {m.is_free ? 'free' : 'paid'}
                                                            </span>
                                                            {s?.rate_limited_now && (
                                                                <span className="px-1.5 py-0.5 rounded-full text-[10px] font-medium bg-amber-100 text-amber-700"
                                                                    title={s.cooldown_until ? `Cooling down until ${new Date(s.cooldown_until).toLocaleTimeString()}` : ''}>
                                                                    ⏳ rate limited
                                                                </span>
                                                            )}
                                                        </div>
                                                        <span className="text-gray-400">{m.provider?.name || s?.provider_name}</span>
                                                    </td>
                                                    <td className="py-1.5 pr-2">{s ? <WindowCell w={s.window_5h} /> : <span className="text-gray-300">—</span>}</td>
                                                    <td className="py-1.5 pr-2">{s ? <WindowCell w={s.window_3d} /> : <span className="text-gray-300">—</span>}</td>
                                                    <td className="py-1.5 text-right text-gray-700">
                                                        {s && s.window_3d.avg_tokens_per_sec > 0 ? s.window_3d.avg_tokens_per_sec.toFixed(1) : '—'}
                                                    </td>
                                                </tr>
                                            );
                                        })}
                                        {(g.members || []).length === 0 && (
                                            <tr><td colSpan={4} className="py-2 text-gray-400">No members configured.</td></tr>
                                        )}
                                    </tbody>
                                </table>

                                <RequestsChart buckets={stats[g.id]?.buckets || []} />
                            </div>
                        </div>
                    </div>
                );
            })()}
        </div>
    );
};
