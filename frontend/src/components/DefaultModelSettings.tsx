import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { ProviderOrGroupSelect } from './ProviderOrGroupSelect';
import type { ProviderOrGroupValue } from './ProviderOrGroupSelect';
import { modelsOfKind } from '../lib/modelKinds';
import type { ModelKind } from '../lib/modelKinds';

// The model slots, matching db.Purpose* on the backend. Smart models decide,
// cheap models do the work; the other two are optional.
const SLOTS: Record<string, { title: string; description: string; none: string; required: boolean }> = {
    smart: {
        title: 'Smart model',
        description: 'Thinks and decides: refines a task, plans it, re-plans when something fails and verifies the result. It is asked one question at a time and never touches files, so it is called rarely. Use your strongest model.',
        none: 'Required — choose a model',
        required: true,
    },
    cheap: {
        title: 'Cheap model',
        description: 'Does the work: research, code, reviews. It runs long sessions with every tool, so most tokens are spent here. Use a fast, inexpensive model.',
        none: 'Required — choose a model',
        required: true,
    },
    classifier: {
        title: 'Classifier (optional)',
        description: 'A System One model (TypeSafe\'s Jev) that answers yes/no questions about a running session: has it just decided something worth recording, is it going in circles. It is not a language model, so only System One models and groups of them are offered here. Without one the engine uses fixed rules instead.',
        none: 'Not used',
        required: false,
    },
    commit_messages: {
        title: 'Commit messages (optional)',
        description: 'Summarizes a task\'s code changes into a git commit message.',
        none: 'Use the cheap model',
        required: false,
    },
};
const SLOT_ORDER = ['smart', 'cheap', 'classifier', 'commit_messages'];

const toFormValue = (s: any): ProviderOrGroupValue => ({
    provider_id: s?.provider_id?.toString() || '',
    model_group_id: s?.model_group_id?.toString() || '',
    model: s?.model || '',
});

interface DefaultModelSettingsProps {
    providers: any[];
    refreshSignal?: number;
    // Opens the form for connecting a classifier provider.
    onConnectClassifier?: () => void;
    // Called after a slot is saved.
    onSaved?: () => void;
}

export const DefaultModelSettings: React.FC<DefaultModelSettingsProps> = ({ providers, refreshSignal, onConnectClassifier, onSaved }) => {
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
            const list = (settingsRes.data || [])
                .filter((s: any) => SLOTS[s.purpose])
                .sort((a: any, b: any) => SLOT_ORDER.indexOf(a.purpose) - SLOT_ORDER.indexOf(b.purpose));
            setSettings(list);
            setModelGroups(groupsRes.data || []);
            setForms(Object.fromEntries(list.map((s: any) => [s.purpose, toFormValue(s)])));
        } catch (e) {
            console.error(e);
        }
    }, []);

    // refreshSignal changes whenever a model group is created/edited/deleted
    // elsewhere on the page, so a slot pointed at a deleted group shows as
    // unset immediately (the backend already reset it via an ON DELETE SET
    // NULL foreign key) instead of only after a page reload. A provider
    // added or removed can fill or empty a slot too.
    useEffect(() => {
        fetchAll();
    }, [fetchAll, refreshSignal, providers.length]);

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
            onSaved?.();
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
                    Tasks run on two tiers of model. A task can name its own model; otherwise it uses these. A tier with no model keeps its tasks waiting, visibly, until one is set.
                </p>
            </div>

            <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
                {settings.map(s => {
                    const info = SLOTS[s.purpose];
                    const value = forms[s.purpose] || toFormValue(s);
                    const unset = !value.provider_id && !value.model_group_id;
                    // The classifier slot takes a System One model, or a group
                    // of them, from whichever provider serves one; the other
                    // slots take language models.
                    const isClassifierSlot = s.purpose === 'classifier';
                    const kind: ModelKind = isClassifierSlot ? 'system_one' : 'llm';
                    const slotProviders = providers.filter(p => modelsOfKind(p, kind).length > 0);
                    return (
                        <div key={s.purpose} data-testid={`model-slot-${s.purpose}`} className={`bg-white p-6 rounded-lg border shadow-sm space-y-3 ${(info.required || isClassifierSlot) && unset ? 'border-amber-300' : ''}`}>
                            <div>
                                <h3 className="text-lg font-bold text-gray-900">{info.title}</h3>
                                {info.description && <p className="text-sm text-gray-600 mt-1">{info.description}</p>}
                            </div>
                            <ProviderOrGroupSelect
                                label="Provider or Model Group"
                                kind={kind}
                                providers={providers}
                                modelGroups={modelGroups}
                                noneLabel={info.none}
                                value={value}
                                onChange={v => setForms(f => ({ ...f, [s.purpose]: v }))}
                            />
                            {isClassifierSlot && slotProviders.length === 0 && (
                                <p className="text-xs text-gray-600">
                                    None of your providers serves a System One model yet.{' '}
                                    {onConnectClassifier ? (
                                        <button type="button" onClick={onConnectClassifier} className="font-medium text-indigo-600 underline hover:text-indigo-800">Connect one</button>
                                    ) : 'Add one under Add Provider.'}
                                    {' '}TypeSafe serves Jev, and so do OpenCode (jev-1.13-free is free there) and AI Surplus. Once a provider with one is connected, this slot is filled for you.
                                </p>
                            )}
                            <div className="flex items-center gap-3 pt-1">
                                <button
                                    onClick={() => handleSave(s.purpose)}
                                    disabled={savingPurpose === s.purpose || (!!value.provider_id && !value.model)}
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
