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
    // Compact: provider and model dropdowns share one line (no "Model Name"
    // label) and the model-group routing note shrinks to a one-liner.
    compact?: boolean;
}

// Combined "provider or model group" picker, plus a concrete model dropdown
// when a plain provider is selected (a model group routes across its own
// members instead). Shared between agent LLM configuration and the
// app-level Default Models settings so both offer identical, dropdown-only
// (no free-text) selection.
export const ProviderOrGroupSelect: React.FC<Props> = ({
    label, providers, modelGroups, value, onChange, manageLinkTo, noneLabel, modelRequired, compact,
}) => {
    const selectValue = value.model_group_id ? `group:${value.model_group_id}` : (value.provider_id ? `provider:${value.provider_id}` : '');

    const targetSelect = (
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
            className={`border rounded p-2 ${compact ? 'flex-1 min-w-0' : 'w-full'}`}
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
    );

    const modelSelect = value.provider_id ? (
        <select
            required={modelRequired}
            value={value.model || ''}
            onChange={e => onChange({ ...value, model: e.target.value })}
            className={`border rounded p-2 ${compact ? 'flex-1 min-w-0' : 'w-full'}`}
        >
            <option value="">-- Select Model --</option>
            {providers.find(p => p.id.toString() === value.provider_id)?.supported_models?.split(',').map((m: string) => m.trim()).filter((m: string) => m).map((m: string) => (
                <option key={m} value={m}>{m}</option>
            ))}
        </select>
    ) : null;

    const groupInfo = value.model_group_id ? (
        compact ? (
            <p className="text-xs text-gray-500 mt-1 truncate" title={describeGroupModels(modelGroups.find(g => g.id.toString() === value.model_group_id))}>
                Auto-routed (free first, failover on errors/rate limits): <span className="font-mono">{describeGroupModels(modelGroups.find(g => g.id.toString() === value.model_group_id))}</span>
            </p>
        ) : (
            <div className="text-xs text-gray-600 bg-indigo-50 border border-indigo-100 rounded p-3 mt-2">
                Requests are routed automatically across this group's models (free first), with retries and failover on errors or rate limits:
                <span className="block mt-1 font-mono break-words">
                    {describeGroupModels(modelGroups.find(g => g.id.toString() === value.model_group_id))}
                </span>
            </div>
        )
    ) : null;

    return (
        <div>
            {(label || manageLinkTo) && (
                <div className="flex justify-between items-center mb-1">
                    <label className="block text-sm font-medium text-gray-700">{label}</label>
                    {manageLinkTo && <Link to={manageLinkTo} className="text-xs text-indigo-600 hover:text-indigo-800">Manage Providers</Link>}
                </div>
            )}

            {compact ? (
                <div className="flex gap-2">
                    {targetSelect}
                    {modelSelect}
                </div>
            ) : (
                <>
                    {targetSelect}
                    {modelSelect && (
                        <div className="mt-2">
                            <label className="block text-sm font-medium text-gray-700 mb-1">Model Name</label>
                            {modelSelect}
                        </div>
                    )}
                </>
            )}

            {groupInfo}
        </div>
    );
};
