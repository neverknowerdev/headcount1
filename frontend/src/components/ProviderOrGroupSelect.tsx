import React from 'react';
import { Link } from 'react-router-dom';
import { groupKind, modelsOfKind } from '../lib/modelKinds';
import type { ModelKind } from '../lib/modelKinds';

// Describes one model group's members as a short label list, showing "Any
// model" for wildcard (all_models) members instead of a blank model name.
export const describeGroupModels = (group: any): string =>
    (group?.members || []).map((m: any) => (m.all_models ? 'Any model' : m.model)).join(', ') || 'no models configured';

export interface ProviderOrGroupValue {
    provider_id: string;
    model_group_id: string;
    model: string;
}

interface Props {
    label: string;
    providers: any[];
    modelGroups: any[];
    value: ProviderOrGroupValue;
    onChange: (v: ProviderOrGroupValue) => void;
    // Link to the LLM Providers page, shown next to the label.
    manageLinkTo?: string;
    // Placeholder text for the "nothing selected" option — customize per
    // use case (e.g. an agent requires a choice; a default-model override
    // can be left blank to fall back to the calling session's own LLM).
    noneLabel?: string;
    modelRequired?: boolean;
    // The kind of model being chosen: language models unless said otherwise.
    // Only providers that have a model of this kind, only their models of
    // this kind, and only groups of this kind are offered.
    kind?: ModelKind;
}

// Combined "provider or model group" picker, plus a concrete model dropdown
// when a plain provider is selected (a model group routes across its own
// members instead). Shared between agent LLM configuration and the
// app-level Default Models settings so both offer identical, dropdown-only
// (no free-text) selection.
export const ProviderOrGroupSelect: React.FC<Props> = ({
    label, providers: allProviders, modelGroups: allGroups, value, onChange, manageLinkTo, noneLabel, modelRequired, kind = 'llm',
}) => {
    // What is selected stays in the list even if it no longer fits, so that
    // a stale choice is shown for what it is rather than as nothing.
    const providers = allProviders.filter(p => modelsOfKind(p, kind).length > 0 || p.id.toString() === value.provider_id);
    const modelGroups = allGroups.filter(g => groupKind(g) === kind || g.id.toString() === value.model_group_id);
    const selectValue = value.model_group_id ? `group:${value.model_group_id}` : (value.provider_id ? `provider:${value.provider_id}` : '');

    return (
        <div>
            <div className="flex justify-between items-center mb-1">
                <label className="block text-sm font-medium text-gray-700">{label}</label>
                {manageLinkTo && <Link to={manageLinkTo} className="text-xs text-indigo-600 hover:text-indigo-800">Manage Providers</Link>}
            </div>
            <select
                value={selectValue}
                onChange={e => {
                    const v = e.target.value;
                    if (v.startsWith('group:')) {
                        onChange({ provider_id: '', model_group_id: v.slice(6), model: '' });
                    } else if (v.startsWith('provider:')) {
                        const selectedProviderId = v.slice(9);
                        const provider = providers.find(p => p.id.toString() === selectedProviderId);
                        const models = modelsOfKind(provider, kind);
                        // The provider's default when it is of this kind; else
                        // a free model of the kind if there is one, else the first.
                        const model = models.includes(provider?.default_model)
                            ? provider.default_model
                            : (models.find(m => m.toLowerCase().includes('free')) || models[0] || '');
                        onChange({ provider_id: selectedProviderId, model_group_id: '', model });
                    } else {
                        onChange({ provider_id: '', model_group_id: '', model: '' });
                    }
                }}
                className="w-full border rounded p-2"
            >
                <option value="">{noneLabel || '-- Select Provider or Group --'}</option>
                {modelGroups.length > 0 && (
                    <optgroup label={kind === 'system_one' ? 'System One groups (auto-routing & failover)' : 'Model Groups (auto-routing & failover)'}>
                        {modelGroups.map(g => <option key={g.id} value={`group:${g.id}`}>{g.name}</option>)}
                    </optgroup>
                )}
                <optgroup label="Providers">
                    {providers.map(p => <option key={p.id} value={`provider:${p.id}`}>{p.name}</option>)}
                </optgroup>
            </select>

            {value.model_group_id ? (
                <div className="text-xs text-gray-600 bg-indigo-50 border border-indigo-100 rounded p-3 mt-2">
                    Requests are routed automatically across this group's models (free first), with retries and failover on errors or rate limits:
                    <span className="block mt-1 font-mono break-words">
                        {describeGroupModels(modelGroups.find(g => g.id.toString() === value.model_group_id))}
                    </span>
                </div>
            ) : value.provider_id ? (
                <div className="mt-2">
                    <label className="block text-sm font-medium text-gray-700 mb-1">{kind === 'system_one' ? 'System One model' : 'Model Name'}</label>
                    <select required={modelRequired} value={value.model || ''} onChange={e => onChange({ ...value, model: e.target.value })} className="w-full border rounded p-2">
                        <option value="">-- Select Model --</option>
                        {modelsOfKind(providers.find(p => p.id.toString() === value.provider_id), kind).map((m: string) => (
                            <option key={m} value={m}>{m}</option>
                        ))}
                    </select>
                </div>
            ) : null}
        </div>
    );
};
