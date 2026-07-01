import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { Plus, Trash2, Edit2, Play, Minus } from 'lucide-react';

interface RoleModelConfig {
    smart_planner_provider_id: number;
    smart_planner_model: string;
    tech_researcher_provider_id: number;
    tech_researcher_model: string;
    writing_researcher_provider_id: number;
    writing_researcher_model: string;
    design_researcher_provider_id: number;
    design_researcher_model: string;
    coder_provider_id: number;
    coder_model: string;
    tester_provider_id: number;
    tester_model: string;
}

const ROLE_MODEL_ROWS = [
    { label: 'Smart Planner', provKey: 'smart_planner_provider_id' as const, modelKey: 'smart_planner_model' as const },
    { label: 'Tech Researcher', provKey: 'tech_researcher_provider_id' as const, modelKey: 'tech_researcher_model' as const },
    { label: 'Writing Researcher', provKey: 'writing_researcher_provider_id' as const, modelKey: 'writing_researcher_model' as const },
    { label: 'Design Researcher', provKey: 'design_researcher_provider_id' as const, modelKey: 'design_researcher_model' as const },
    { label: 'Coder', provKey: 'coder_provider_id' as const, modelKey: 'coder_model' as const },
    { label: 'Tester', provKey: 'tester_provider_id' as const, modelKey: 'tester_model' as const },
] as const;

export const ProvidersManager: React.FC = () => {
    const [providers, setProviders] = useState<any[]>([]);
    const [isModalOpen, setIsModalOpen] = useState(false);
    const [editingId, setEditingId] = useState<number | null>(null);
    const [formData, setFormData] = useState({ name: '', base_url: '', api_key: '', default_model: '', provider_type: '' });
    const [originalModels, setOriginalModels] = useState<string[]>([]);
    const [supportedModels, setSupportedModels] = useState<string[]>(['']);
    const [testResult, setTestResult] = useState<{status?: string, error?: string, log?: string} | null>(null);
    const [isSaving, setIsSaving] = useState(false);
    const [testingProgress, setTestingProgress] = useState<string>('');

    // Role Model Configuration — which provider/model powers each AI role in the task pipeline.
    // fullSettings holds the complete /api/settings payload so saving role_models or the
    // default provider doesn't clobber unrelated fields (POST /api/settings fully overwrites
    // settings.yaml rather than merging).
    const [fullSettings, setFullSettings] = useState<any>(null);
    const [defaultProviderId, setDefaultProviderId] = useState<number>(0);
    const [defaultProviderSaveState, setDefaultProviderSaveState] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');
    const [roleModels, setRoleModels] = useState<RoleModelConfig>({
        smart_planner_provider_id: 0, smart_planner_model: '',
        tech_researcher_provider_id: 0, tech_researcher_model: '',
        writing_researcher_provider_id: 0, writing_researcher_model: '',
        design_researcher_provider_id: 0, design_researcher_model: '',
        coder_provider_id: 0, coder_model: '',
        tester_provider_id: 0, tester_model: '',
    });
    const [roleModelsSaveState, setRoleModelsSaveState] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');

    const fetchProviders = async () => {
        try {
            const res = await axios.get('/api/providers');
            setProviders(res.data || []);
        } catch (e) {
            console.error(e);
        }
    };

    const fetchSettings = async () => {
        try {
            const res = await axios.get('/api/settings');
            setFullSettings(res.data || {});
            setDefaultProviderId(res.data?.default_provider_id || 0);
            if (res.data?.role_models) {
                setRoleModels(prev => ({ ...prev, ...res.data.role_models }));
            }
        } catch (e) {
            console.error(e);
        }
    };

    useEffect(() => {
        fetchProviders();
        fetchSettings();
    }, []);

    // Defensive fallback for setups that had providers configured before the
    // default-provider invariant existed: if no default is set yet but a
    // provider is available, default the (unsaved) selection to the first one
    // rather than showing a blank/mismatched dropdown.
    useEffect(() => {
        if (!defaultProviderId && providers.length > 0) {
            setDefaultProviderId(providers[0].id);
        }
    }, [providers, defaultProviderId]);

    const handleSaveDefaultProvider = async () => {
        setDefaultProviderSaveState('saving');
        try {
            await axios.post('/api/settings', { ...fullSettings, default_provider_id: defaultProviderId });
            setDefaultProviderSaveState('saved');
            setTimeout(() => setDefaultProviderSaveState('idle'), 2000);
        } catch (e) {
            console.error(e);
            setDefaultProviderSaveState('error');
        }
    };

    const handleSaveRoleModels = async () => {
        setRoleModelsSaveState('saving');
        try {
            await axios.post('/api/settings', { ...fullSettings, role_models: roleModels });
            setRoleModelsSaveState('saved');
            setTimeout(() => setRoleModelsSaveState('idle'), 2000);
        } catch (e) {
            console.error(e);
            setRoleModelsSaveState('error');
        }
    };

    const testSingleModel = async (model: string, base_url: string, api_key: string, provider_type: string, provider_id?: number | null) => {
        try {
            const res = await axios.post('/api/providers/test', {
                provider_id,
                base_url,
                api_key,
                model,
                provider_type
            });
            return res.data;
        } catch (e: any) {
            throw new Error(`Model ${model} failed: ${e.response?.data?.error || 'Unknown error'}`);
        }
    };

    const handleOpenModal = (p?: any) => {
        setTestResult(null);
        setTestingProgress('');
        if (p) {
            setEditingId(p.id);
            setFormData({ name: p.name, base_url: p.base_url, api_key: '', default_model: p.default_model || '', provider_type: p.provider_type || '' });
            const existingModels = p.supported_models ? p.supported_models.split(',').map((m:string) => m.trim()) : [];
            setSupportedModels(existingModels.length ? existingModels : ['']);
            setOriginalModels(existingModels);
        } else {
            setEditingId(null);
            setFormData({ name: '', base_url: '', api_key: '', default_model: '', provider_type: '' });
            setSupportedModels(['']);
            setOriginalModels([]);
        }
        setIsModalOpen(true);
    };

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        setIsSaving(true);
        setTestResult(null);
        setTestingProgress('');

        try {
            const modelsToTest = [];
            if (formData.default_model) modelsToTest.push(formData.default_model.trim());
            const validSupported = supportedModels.map(m => m.trim()).filter(m => m);
            modelsToTest.push(...validSupported);

            // Deduplicate models
            const uniqueModels = Array.from(new Set(modelsToTest));

            // Determine which models actually need testing (the new ones or if API key is new)
            // If editing and no new API key, only test newly added models.
            // But we can't test without API key if it's hidden. So only test if we have an API key or if they are NEW models.
            // Since we can't test new models without an API key, require API key if new models are added during edit.

            const newModels = uniqueModels.filter(m => !originalModels.includes(m) && m !== formData.default_model); // Also consider default_model test if changed

            let detectedProviderType = formData.provider_type;
            let detectedBaseUrl = formData.base_url;

            const modelsToActuallyTest = (!editingId || formData.api_key) ? uniqueModels : newModels;

            for (const model of modelsToActuallyTest) {
                setTestingProgress(`Testing model: ${model}...`);
                const testRes = await testSingleModel(model, detectedBaseUrl, formData.api_key, detectedProviderType, editingId);
                if (testRes && testRes.provider_type) {
                    detectedProviderType = testRes.provider_type;
                }
                if (testRes && testRes.url) {
                    detectedBaseUrl = testRes.url;
                }
            }
            setTestingProgress('');

            const finalData = {
                ...formData,
                base_url: detectedBaseUrl,
                provider_type: detectedProviderType,
                supported_models: validSupported.join(',')
            };

            if (editingId) {
                await axios.put(`/api/providers/${editingId}`, finalData);
            } else {
                await axios.post('/api/providers', finalData);
            }

            setIsModalOpen(false);
            fetchProviders();
        } catch (e: any) {
            console.error(e);
            setTestResult({ error: e.message || "Save failed" });
        } finally {
            setIsSaving(false);
            setTestingProgress('');
        }
    };

    const handleDelete = async (id: number) => {
        if (!window.confirm("Are you sure?")) return;
        try {
            await axios.delete(`/api/providers/${id}`);
            fetchProviders();
        } catch (e) {
            console.error(e);
        }
    };

    const handleTest = async (provider: any) => {
        setTestResult(null);
        setTestingProgress(`Testing ${provider.default_model || 'gpt-3.5-turbo'}...`);
        try {
            const modelToTest = provider.default_model || 'gpt-3.5-turbo';
            const res = await testSingleModel(modelToTest, provider.base_url, '', provider.provider_type, provider.id);
            setTestResult(res);
        } catch (e: any) {
            setTestResult({ error: e.message || 'Unknown error' });
        } finally {
            setTestingProgress('');
        }
    };

    const updateSupportedModel = (index: number, value: string) => {
        const newModels = [...supportedModels];
        newModels[index] = value;
        setSupportedModels(newModels);
    };

    const addSupportedModel = () => {
        setSupportedModels([...supportedModels, '']);
    };

    const removeSupportedModel = (index: number) => {
        const newModels = [...supportedModels];
        newModels.splice(index, 1);
        if (newModels.length === 0) newModels.push('');
        setSupportedModels(newModels);
    };

    return (
        <div className="h-full flex flex-col space-y-6">
            <div className="flex justify-between items-center">
                <h1 className="text-2xl font-bold">LLM Providers</h1>
                <button
                    onClick={() => handleOpenModal()}
                    className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700 shadow-sm"
                >
                    <Plus size={16} className="mr-2" /> Add Provider
                </button>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
                {providers.map(p => (
                    <div key={p.id} className="bg-white p-6 rounded-lg border shadow-sm flex flex-col relative overflow-hidden">
                        <div className="flex justify-between items-start mb-4">
                            <h3 className="text-lg font-bold text-gray-900">{p.name}</h3>
                            <div className="flex space-x-2">
                                <button onClick={() => handleTest(p)} className="text-blue-500 hover:text-blue-700" title="Test Connection">
                                    <Play size={18} />
                                </button>
                                <button onClick={() => handleOpenModal(p)} className="text-gray-500 hover:text-gray-700">
                                    <Edit2 size={18} />
                                </button>
                                <button onClick={() => handleDelete(p.id)} className="text-red-500 hover:text-red-700">
                                    <Trash2 size={18} />
                                </button>
                            </div>
                        </div>
                        <p className="text-sm text-gray-600 mb-1 truncate"><span className="font-semibold">URL:</span> {p.base_url}</p>
                        {p.default_model && <p className="text-sm text-gray-600 mb-1"><span className="font-semibold">Default Model:</span> {p.default_model}</p>}
                        {p.supported_models && <p className="text-xs text-gray-500 mt-2 break-words"><span className="font-semibold">Models:</span> {p.supported_models.split(',').join(', ')}</p>}
                    </div>
                ))}
            </div>

            {providers.length > 0 && (
                <div className="bg-white p-6 rounded-lg shadow-sm border">
                    <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4">Default Provider</h2>
                    <p className="text-xs text-gray-500 mb-4">
                        Used by any AI role that doesn't have a specific provider set below in Role Model Configuration.
                    </p>
                    <div className="flex items-center gap-3 max-w-md">
                        <select
                            value={defaultProviderId || 0}
                            onChange={e => setDefaultProviderId(parseInt(e.target.value))}
                            className="flex-1 border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border bg-white"
                        >
                            {providers.map(p => (
                                <option key={p.id} value={p.id}>{p.name}</option>
                            ))}
                        </select>
                        <button
                            type="button"
                            onClick={handleSaveDefaultProvider}
                            disabled={defaultProviderSaveState === 'saving'}
                            className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700 disabled:bg-indigo-400 shrink-0"
                        >
                            {defaultProviderSaveState === 'saving' ? 'Saving...' : 'Save'}
                        </button>
                        {defaultProviderSaveState === 'saved' && <span className="text-sm text-green-600 shrink-0">Saved</span>}
                        {defaultProviderSaveState === 'error' && <span className="text-sm text-red-600 shrink-0">Failed to save</span>}
                    </div>
                </div>
            )}

            {testingProgress && !isModalOpen && (
                <div className="mt-4 p-4 rounded bg-blue-50 text-blue-800">
                    <p className="text-sm font-semibold">{testingProgress}</p>
                </div>
            )}

            {testResult && !isModalOpen && (
                <div className={`mt-4 p-4 rounded ${testResult.error ? 'bg-red-50 text-red-800' : 'bg-green-50 text-green-800'}`}>
                    <h3 className="font-bold mb-2">{testResult.error ? 'Test Failed' : 'Test Succeeded'}</h3>
                    {testResult.error && <p className="text-sm mb-2">{testResult.error}</p>}
                    {testResult.log && (
                        <pre className="text-xs mt-2 overflow-x-auto whitespace-pre-wrap">{testResult.log}</pre>
                    )}
                </div>
            )}

            {isModalOpen && (
                <div className="fixed inset-0 bg-black/50 flex justify-center items-center z-50">
                    <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-lg max-h-[90vh] flex flex-col">
                        <h2 className="text-xl font-bold mb-4">{editingId ? 'Edit Provider' : 'Add Provider'}</h2>
                        <form onSubmit={handleSubmit} className="space-y-4 overflow-y-auto flex-1 pr-2">
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Name</label>
                                <input required type="text" value={formData.name} onChange={e => setFormData({...formData, name: e.target.value})} className="w-full border rounded p-2" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Base URL</label>
                                <input required type="text" value={formData.base_url} onChange={e => setFormData({...formData, base_url: e.target.value})} className="w-full border rounded p-2" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">API Key {editingId && '(Leave blank to keep existing)'}</label>
                                <input type="password" required={!editingId} value={formData.api_key} onChange={e => setFormData({...formData, api_key: e.target.value})} className="w-full border rounded p-2" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Default Model</label>
                                <input required type="text" value={formData.default_model} onChange={e => setFormData({...formData, default_model: e.target.value})} placeholder="e.g. gpt-4o" className="w-full border rounded p-2" />
                            </div>

                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-2">Supported Models</label>
                                {supportedModels.map((m, idx) => (
                                    <div key={idx} className="flex items-center space-x-2 mb-2">
                                        <input
                                            type="text"
                                            value={m}
                                            onChange={e => updateSupportedModel(idx, e.target.value)}
                                            placeholder="Model name (e.g. gpt-3.5-turbo)"
                                            className="flex-1 border rounded p-2 text-sm"
                                        />
                                        <button type="button" onClick={() => removeSupportedModel(idx)} className="text-red-500 hover:text-red-700 p-2 border border-transparent hover:border-red-200 rounded">
                                            <Minus size={16} />
                                        </button>
                                    </div>
                                ))}
                                <button type="button" onClick={addSupportedModel} className="text-indigo-600 hover:text-indigo-800 text-sm font-medium flex items-center mt-2">
                                    <Plus size={14} className="mr-1" /> Add Model
                                </button>
                                <p className="text-xs text-gray-500 mt-2">All configured models will be tested before saving.</p>
                            </div>

                            {testingProgress && (
                                <div className="mt-4 p-3 rounded bg-blue-50 text-blue-800 border border-blue-200 text-sm font-medium flex items-center">
                                    <svg className="animate-spin -ml-1 mr-3 h-4 w-4 text-blue-600" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
                                        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4"></circle>
                                        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
                                    </svg>
                                    {testingProgress}
                                </div>
                            )}

                            {testResult && (
                                <div className={`mt-4 p-3 rounded text-sm ${testResult.error ? 'bg-red-50 text-red-800 border border-red-200' : 'bg-green-50 text-green-800 border border-green-200'}`}>
                                    <span className="font-bold">{testResult.error ? 'Test Failed: ' : 'Test Succeeded'}</span>
                                    {testResult.error && <span>{testResult.error}</span>}
                                </div>
                            )}

                        </form>
                        <div className="flex justify-end space-x-3 pt-4 border-t mt-4">
                            <button type="button" onClick={() => setIsModalOpen(false)} className="text-gray-500 hover:text-gray-700 px-4 py-2">Cancel</button>
                            <button type="button" onClick={handleSubmit} disabled={isSaving} className={`bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 flex items-center ${isSaving ? 'opacity-50 cursor-not-allowed' : ''}`}>
                                {isSaving ? 'Saving...' : 'Save Provider'}
                            </button>
                        </div>
                    </div>
                </div>
            )}

            <div className="bg-white p-6 rounded-lg shadow-sm border">
                <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4">Role Model Configuration</h2>
                <p className="text-xs text-gray-500 mb-4">
                    Assign a provider and model to each AI role used in the task pipeline. Leave provider at "default provider" to use the Default Provider set above.
                </p>
                <div className="space-y-3 max-w-2xl">
                    {ROLE_MODEL_ROWS.map(({ label, provKey, modelKey }) => {
                        const roleProviderId = roleModels[provKey];
                        // When the role has no explicit provider, it resolves to the
                        // Default Provider — show that provider's models, not a blank state.
                        const effectiveProvider = providers.find(p => p.id === (roleProviderId || defaultProviderId));
                        const modelOptions = effectiveProvider?.supported_models
                            ? effectiveProvider.supported_models.split(',').map((m: string) => m.trim()).filter((m: string) => m)
                            : [];
                        return (
                            <div key={label}>
                                <label className="block text-sm font-medium text-gray-700 mb-1">{label}</label>
                                <div className="flex gap-2">
                                    <select
                                        value={roleProviderId || 0}
                                        onChange={e => setRoleModels(prev => ({ ...prev, [provKey]: parseInt(e.target.value) }))}
                                        className="w-48 border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border bg-white"
                                    >
                                        <option value={0}>default provider</option>
                                        {providers.map(p => (
                                            <option key={p.id} value={p.id}>{p.name}</option>
                                        ))}
                                    </select>
                                    {effectiveProvider && modelOptions.length > 0 ? (
                                        <select
                                            value={roleModels[modelKey] || ''}
                                            onChange={e => setRoleModels(prev => ({ ...prev, [modelKey]: e.target.value }))}
                                            className="flex-1 border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border bg-white"
                                        >
                                            <option value="">provider's default{effectiveProvider.default_model ? ` (${effectiveProvider.default_model})` : ''}</option>
                                            {modelOptions.map((m: string) => (
                                                <option key={m} value={m}>{m}</option>
                                            ))}
                                        </select>
                                    ) : (
                                        <input
                                            type="text"
                                            value={roleModels[modelKey] || ''}
                                            onChange={e => setRoleModels(prev => ({ ...prev, [modelKey]: e.target.value }))}
                                            disabled={!effectiveProvider}
                                            className="flex-1 border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border disabled:bg-gray-50 disabled:text-gray-400"
                                            placeholder={effectiveProvider ? "model name (provider's default used if blank)" : 'select a provider first'}
                                        />
                                    )}
                                </div>
                            </div>
                        );
                    })}
                </div>
                <div className="mt-5 flex items-center gap-3">
                    <button
                        type="button"
                        onClick={handleSaveRoleModels}
                        disabled={roleModelsSaveState === 'saving'}
                        className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700 disabled:bg-indigo-400"
                    >
                        {roleModelsSaveState === 'saving' ? 'Saving...' : 'Save Role Model Configuration'}
                    </button>
                    {roleModelsSaveState === 'saved' && <span className="text-sm text-green-600">Saved</span>}
                    {roleModelsSaveState === 'error' && <span className="text-sm text-red-600">Failed to save</span>}
                </div>
            </div>
        </div>
    );
};
