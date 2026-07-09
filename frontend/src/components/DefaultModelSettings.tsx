import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { ProviderOrGroupSelect } from './ProviderOrGroupSelect';
import type { ProviderOrGroupValue } from './ProviderOrGroupSelect';

// Purposes with a configurable default model, matching db.Purpose* on the
// backend. Add an entry here whenever a new one-shot internal LLM use case
// gets its own "Default Models" row.
const PURPOSE_LABELS: Record<string, { title: string; description: string }> = {
    commit_messages: {
        title: 'Commit Messages',
        description: 'Summarizes a task\'s code changes into a git commit message after a run finishes.',
    },
    ask_artifact: {
        title: 'Artifact Q&A (ask_artifact)',
        description: 'Answers an agent\'s question about one artifact via a separate one-shot reader call, so the artifact never enters the asking session\'s context.',
    },
};

// The three Hindsight memory-backend purposes, rendered together as one
// combined "Long-term Memory (Hindsight)" card instead of separate cards.
// Retain doubles as the fallback LLM: hindsight-api falls back to it for
// consolidation/reflect whenever those aren't explicitly configured.
const HINDSIGHT_ROWS: { purpose: string; label: string; description: string; noneLabel: string }[] = [
    {
        purpose: 'hindsight_retain',
        label: 'Retain',
        description: 'Extracts facts, entities and relationships from new content as it\'s ingested. High volume — a fast, cheap model is usually the best fit.',
        noneLabel: '-- Not configured (memory runs without an LLM) --',
    },
    {
        purpose: 'hindsight_consolidation',
        label: 'Consolidation',
        description: 'Deduplicates and merges memory records. Falls back to the Retain model when left unset.',
        noneLabel: '-- Fall back to Retain\'s model --',
    },
    {
        purpose: 'hindsight_reflect',
        label: 'Reflect',
        description: 'Reasons across existing memories to form new connections (e.g. the "Ask memory" action). Called rarely — a stronger reasoning model pays off here. Falls back to the Retain model when left unset.',
        noneLabel: '-- Fall back to Retain\'s model --',
    },
];
const HINDSIGHT_PURPOSES = new Set(HINDSIGHT_ROWS.map(r => r.purpose));

const toFormValue = (s: any): ProviderOrGroupValue => ({
    provider_id: s?.provider_id?.toString() || '',
    model_group_id: s?.model_group_id?.toString() || '',
    model: s?.model || '',
});

export const DefaultModelSettings: React.FC<{ providers: any[]; refreshSignal?: number }> = ({ providers, refreshSignal }) => {
    const [settings, setSettings] = useState<any[]>([]);
    const [modelGroups, setModelGroups] = useState<any[]>([]);
    const [forms, setForms] = useState<Record<string, ProviderOrGroupValue>>({});
    const [savingPurpose, setSavingPurpose] = useState<string | null>(null);
    const [savedPurpose, setSavedPurpose] = useState<string | null>(null);
    const [error, setError] = useState<string | null>(null);

    const fetchAll = useCallback(async () => {
        try {
            const [settingsRes, groupsRes] = await Promise.all([
                axios.get('/api/default-model-settings'),
                axios.get('/api/model-groups'),
            ]);
            const list = settingsRes.data || [];
            setSettings(list);
            setModelGroups(groupsRes.data || []);
            setForms(Object.fromEntries(list.map((s: any) => [s.purpose, toFormValue(s)])));
        } catch (e) {
            console.error(e);
        }
    }, []);

    // refreshSignal changes whenever a model group is created/edited/deleted
    // elsewhere on the page, so a purpose pointed at a deleted group shows
    // "Session's own model" immediately (the backend already reset it via an
    // ON DELETE SET NULL foreign key) instead of only after a page reload.
    useEffect(() => {
        fetchAll();
    }, [fetchAll, refreshSignal]);

    const handleSave = async (purpose: string) => {
        setSavingPurpose(purpose);
        setSavedPurpose(null);
        setError(null);
        const v = forms[purpose];
        try {
            await axios.put(`/api/default-model-settings/${purpose}`, {
                provider_id: v.model_group_id ? null : (v.provider_id ? parseInt(v.provider_id) : null),
                model: v.model_group_id ? '' : v.model,
                model_group_id: v.model_group_id ? parseInt(v.model_group_id) : null,
            });
            setSavedPurpose(purpose);
            setTimeout(() => setSavedPurpose(p => (p === purpose ? null : p)), 2000);
            fetchAll();
            // Tell the app-level memory alert (Layout) to re-check whether a
            // retain model is now configured.
            window.dispatchEvent(new Event('default-model-settings-changed'));
        } catch (e: any) {
            setError(e.response?.data?.error || 'Save failed');
        } finally {
            setSavingPurpose(null);
        }
    };

    if (settings.length === 0) return null;

    const otherSettings = settings.filter(s => !HINDSIGHT_PURPOSES.has(s.purpose));
    const hindsightSettings = settings.filter(s => HINDSIGHT_PURPOSES.has(s.purpose));

    const saveButton = (purpose: string) => (
        <div className="flex items-center gap-3 pt-1">
            <button
                onClick={() => handleSave(purpose)}
                disabled={savingPurpose === purpose}
                className={`bg-indigo-600 text-white px-3 py-1.5 rounded text-sm hover:bg-indigo-700 ${savingPurpose === purpose ? 'opacity-50 cursor-not-allowed' : ''}`}
            >
                {savingPurpose === purpose ? 'Saving...' : 'Save'}
            </button>
            {savedPurpose === purpose && <span className="text-sm text-green-600">Saved</span>}
        </div>
    );

    return (
        <div className="space-y-4">
            <div>
                <h2 className="text-xl font-bold">Default Models</h2>
                <p className="text-sm text-gray-500">
                    The provider/model or model group used for specific internal, lightweight LLM calls. Leave a purpose unset to fall back to the calling session's own LLM.
                </p>
            </div>

            <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
                {otherSettings.map(s => {
                    const info = PURPOSE_LABELS[s.purpose] || { title: s.purpose, description: '' };
                    const value = forms[s.purpose] || toFormValue(s);
                    return (
                        <div key={s.purpose} className="bg-white p-4 rounded-lg border shadow-sm space-y-2">
                            <div>
                                <h3 className="text-base font-bold text-gray-900">{info.title}</h3>
                                {info.description && <p className="text-xs text-gray-500 mt-0.5">{info.description}</p>}
                            </div>
                            <div className="flex items-start gap-3">
                                <div className="flex-1 min-w-0">
                                    <ProviderOrGroupSelect
                                        label=""
                                        compact
                                        providers={providers}
                                        modelGroups={modelGroups}
                                        noneLabel="Session's own model (no override)"
                                        value={value}
                                        onChange={v => setForms(f => ({ ...f, [s.purpose]: v }))}
                                    />
                                </div>
                                {saveButton(s.purpose)}
                            </div>
                        </div>
                    );
                })}

                {hindsightSettings.length > 0 && (
                    <div className="bg-white p-4 rounded-lg border shadow-sm space-y-3 xl:col-span-2">
                        <div>
                            <h3 className="text-base font-bold text-gray-900">Long-term Memory (Hindsight)</h3>
                            <p className="text-xs text-gray-500 mt-0.5">
                                The model the memory backend uses for each stage: retain (fact extraction), consolidation and reflect.
                            </p>
                        </div>
                        {HINDSIGHT_ROWS.map(row => {
                            const s = hindsightSettings.find(x => x.purpose === row.purpose);
                            if (!s) return null;
                            const value = forms[row.purpose] || toFormValue(s);
                            return (
                                <div key={row.purpose}>
                                    <div className="flex items-start gap-3">
                                        <label className="w-28 shrink-0 pt-2 text-sm font-medium text-gray-700">{row.label}</label>
                                        <div className="flex-1 min-w-0">
                                            <ProviderOrGroupSelect
                                                label=""
                                                compact
                                                providers={providers}
                                                modelGroups={modelGroups}
                                                noneLabel={row.noneLabel}
                                                value={value}
                                                onChange={v => setForms(f => ({ ...f, [row.purpose]: v }))}
                                            />
                                        </div>
                                        <button
                                            onClick={() => handleSave(row.purpose)}
                                            disabled={savingPurpose === row.purpose}
                                            className={`bg-indigo-600 text-white px-3 py-1.5 rounded text-sm hover:bg-indigo-700 mt-0.5 ${savingPurpose === row.purpose ? 'opacity-50 cursor-not-allowed' : ''}`}
                                        >
                                            {savingPurpose === row.purpose ? 'Saving...' : 'Save'}
                                        </button>
                                        {savedPurpose === row.purpose && <span className="text-sm text-green-600 pt-2">Saved</span>}
                                    </div>
                                    <p className="text-xs text-gray-500 mt-1 pl-[7.75rem]">{row.description}</p>
                                </div>
                            );
                        })}
                    </div>
                )}
            </div>
            {error && (
                <div className="p-3 rounded text-sm bg-red-50 text-red-800 border border-red-200">{error}</div>
            )}
        </div>
    );
};
