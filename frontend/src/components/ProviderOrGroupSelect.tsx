import React from 'react';
import { Link } from 'react-router-dom';

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
}

// Combined "provider or model group" picker, plus a concrete model dropdown
// when a plain provider is selected (a model group routes across its own
// members instead). Shared between agent LLM configuration and the
// app-level Default Models settings so both offer identical, dropdown-only
// (no free-text) selection.
export const ProviderOrGroupSelect: React.FC<Props> = ({
    label, providers, modelGroups, value, onChange, manageLinkTo, noneLabel, modelRequired,
}) => {
    const selectValue = value.model_group_id ? `group:${value.model_group_id}` : (value.provider_id ? `provider:${value.provider_id}` : '');

    return (
        <div>
            {(label || manageLinkTo) && (
                <div className="flex justify-between items-center mb-1">
                    <label className="block text-sm font-medium text-gray-700">{label}</label>
                    {manageLinkTo && <Link to={manageLinkTo} className="text-xs text-indigo-600 hover:text-indigo-800">Manage Providers</Link>}
                </div>
            )}
            <select
                value={selectValue}
                onChange={e => {
                    const v = e.target.value;
                    if (v.startsWith('group:')) {
                        onChange({ provider_id: '', model_group_id: v.slice(6), model: '' });
                    } else if (v.startsWith('provider:')) {
                        const selectedProviderId = v.slice(9);
                        const provider = providers.find(p => p.id.toString() === selectedProviderId);
                        onChange({ provider_id: selectedProviderId, model_group_id: '', model: provider?.default_model || '' });
                    } else {
                        onChange({ provider_id: '', model_group_id: '', model: '' });
                    }
                }}
                className="w-full border rounded p-2"
            >
                <option value="">{noneLabel || '-- Select Provider or Group --'}</option>
                {modelGroups.length > 0 && (
                    <optgroup label="Model Groups (auto-routing & failover)">
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
                    <label className="block text-sm font-medium text-gray-700 mb-1">Model Name</label>
                    <select required={modelRequired} value={value.model || ''} onChange={e => onChange({ ...value, model: e.target.value })} className="w-full border rounded p-2">
                        <option value="">-- Select Model --</option>
                        {providers.find(p => p.id.toString() === value.provider_id)?.supported_models?.split(',').map((m: string) => m.trim()).filter((m: string) => m).map((m: string) => (
                            <option key={m} value={m}>{m}</option>
                        ))}
                    </select>
                </div>
            ) : null}
        </div>
    );
};
