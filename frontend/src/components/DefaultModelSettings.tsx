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
    task_orchestrator: {
        title: 'Task Orchestrator',
        description: 'Required control-plane model. It selects, starts, monitors, and recovers worker sessions; it never performs implementation work.',
    },
    helper_worker: {
        title: 'Helper Worker Model',
        description: 'Model for bounded ephemeral research and verification workers. Leave unset to use the Task Orchestrator model; this never falls back to the parent agent model.',
    },
};

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
        } catch (e: any) {
            setError(e.response?.data?.error || 'Save failed');
        } finally {
            setSavingPurpose(null);
        }
    };

    if (settings.length === 0) return null;

    return (
        <div className="space-y-4">
            <div>
                <h2 className="text-xl font-bold">Default Models</h2>
                <p className="text-sm text-gray-500">
                    The provider/model or model group used for specific internal, lightweight LLM calls. Leave a purpose unset to fall back to the calling session's own LLM.
                </p>
            </div>

            <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
                {settings.map(s => {
                    const info = PURPOSE_LABELS[s.purpose] || { title: s.purpose, description: '' };
                    const value = forms[s.purpose] || toFormValue(s);
                    const isOrchestrator = s.purpose === 'task_orchestrator';
                    const isHelper = s.purpose === 'helper_worker';
                    return (
                        <div key={s.purpose} className="bg-white p-6 rounded-lg border shadow-sm space-y-3">
                            <div>
                                <h3 className="text-lg font-bold text-gray-900">{info.title}</h3>
                                {info.description && <p className="text-sm text-gray-600 mt-1">{info.description}</p>}
                            </div>
                            <ProviderOrGroupSelect
                                label="Provider or Model Group"
                                providers={providers}
                                modelGroups={modelGroups}
                                noneLabel={isHelper ? 'Use Task Orchestrator model' : (isOrchestrator ? 'Required — choose a model' : "Session's own model (no override)")}
                                value={value}
                                onChange={v => setForms(f => ({ ...f, [s.purpose]: v }))}
                            />
                            <div className="flex items-center gap-3 pt-1">
                                <button
                                    onClick={() => handleSave(s.purpose)}
                                    disabled={savingPurpose === s.purpose || (isOrchestrator && !value.provider_id && !value.model_group_id)}
                                    className={`bg-indigo-600 text-white px-3 py-1.5 rounded text-sm hover:bg-indigo-700 ${savingPurpose === s.purpose ? 'opacity-50 cursor-not-allowed' : ''}`}
                                >
                                    {savingPurpose === s.purpose ? 'Saving...' : 'Save'}
                                </button>
                                {savedPurpose === s.purpose && <span className="text-sm text-green-600">Saved</span>}
                            </div>
                        </div>
                    );
                })}
            </div>
            {error && (
                <div className="p-3 rounded text-sm bg-red-50 text-red-800 border border-red-200">{error}</div>
            )}
        </div>
    );
};
