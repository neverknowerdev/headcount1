import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { Plus, Trash2, Edit2, Play, Pause, Minus, RefreshCw, KeyRound, Zap } from 'lucide-react';

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

    // Built-in providers (OpenRouter/OpenCode free models) get a simplified
    // "Activate" flow instead of the generic edit modal — their model list
    // is discovered automatically, not hand-typed.
    const [rediscoveringId, setRediscoveringId] = useState<number | null>(null);
    const [activateProvider, setActivateProvider] = useState<any | null>(null);
    const [activateApiKey, setActivateApiKey] = useState('');
    const [activateTestResult, setActivateTestResult] = useState<{status?: string, error?: string, log?: string} | null>(null);
    const [isActivateTesting, setIsActivateTesting] = useState(false);
    const [isActivateSaving, setIsActivateSaving] = useState(false);
    const [togglingId, setTogglingId] = useState<number | null>(null);

    const fetchProviders = async () => {
        try {
            const res = await axios.get('/api/providers');
            setProviders(res.data || []);
        } catch (e) {
            console.error(e);
        }
    };

    useEffect(() => {
        fetchProviders();
    }, []);

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

    const handleRediscover = async (provider: any) => {
        setRediscoveringId(provider.id);
        try {
            const res = await axios.post(`/api/providers/${provider.id}/rediscover`);
            setProviders(prev => prev.map(p => p.id === provider.id ? res.data : p));
            setActivateProvider((prev: any) => (prev && prev.id === provider.id) ? res.data : prev);
        } catch (e: any) {
            alert(e.response?.data?.error || 'Failed to re-discover models');
        } finally {
            setRediscoveringId(null);
        }
    };

    const handleToggleEnabled = async (provider: any) => {
        setTogglingId(provider.id);
        try {
            await axios.put(`/api/providers/${provider.id}`, { enabled: !provider.enabled });
            setProviders(prev => prev.map(p => p.id === provider.id ? { ...p, enabled: !p.enabled } : p));
        } catch (e: any) {
            alert(e.response?.data?.error || 'Failed to update provider');
        } finally {
            setTogglingId(null);
        }
    };

    const openActivateModal = (provider: any) => {
        setActivateProvider(provider);
        setActivateApiKey('');
        setActivateTestResult(null);
    };

    const closeActivateModal = () => {
        setActivateProvider(null);
        setActivateApiKey('');
        setActivateTestResult(null);
    };

    const handleTestActivate = async () => {
        if (!activateProvider) return;
        setIsActivateTesting(true);
        setActivateTestResult(null);
        try {
            const res = await testSingleModel(activateProvider.default_model, activateProvider.base_url, activateApiKey, activateProvider.provider_type, activateProvider.id);
            setActivateTestResult(res);
        } catch (e: any) {
            setActivateTestResult({ error: e.message || 'Connection failed. Check your API key and try again.' });
        } finally {
            setIsActivateTesting(false);
        }
    };

    const handleSaveActivate = async () => {
        if (!activateProvider) return;
        setIsActivateSaving(true);
        try {
            // Preserve the provider's own base_url/provider_type — these are
            // known-good for a built-in gateway; the test probe's
            // auto-detected shape isn't a reliable substitute (it races an
            // OpenAI- and an Anthropic-shaped request and could otherwise
            // clobber the URL with the wrong one).
            await axios.put(`/api/providers/${activateProvider.id}`, {
                name: activateProvider.name,
                base_url: activateProvider.base_url,
                api_key: activateApiKey,
                provider_type: activateProvider.provider_type,
                default_model: activateProvider.default_model,
                supported_models: activateProvider.supported_models,
            });
            closeActivateModal();
            fetchProviders();
        } catch (e: any) {
            setActivateTestResult({ error: e.response?.data?.error || 'Save failed' });
        } finally {
            setIsActivateSaving(false);
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
                    <div key={p.id} className={`bg-white p-6 rounded-lg border shadow-sm flex flex-col relative overflow-hidden ${p.builtin && !p.enabled ? 'opacity-60' : ''}`}>
                        <div className="flex justify-between items-start mb-4">
                            <h3 className="text-lg font-bold text-gray-900 flex items-center gap-2 flex-wrap">
                                {p.name}
                                {p.builtin && (
                                    <span className="text-xs font-medium bg-indigo-100 text-indigo-700 px-2 py-0.5 rounded-full">Built-in</span>
                                )}
                                {p.builtin && (
                                    <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${p.enabled ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-500'}`}>
                                        {p.enabled ? 'Enabled' : 'Disabled'}
                                    </span>
                                )}
                            </h3>
                            <div className="flex space-x-2 flex-shrink-0">
                                <button onClick={() => handleTest(p)} className="text-blue-500 hover:text-blue-700" title="Test Connection">
                                    <Zap size={18} />
                                </button>
                                {p.builtin ? (
                                    <>
                                        <button
                                            onClick={() => handleRediscover(p)}
                                            disabled={rediscoveringId === p.id}
                                            className="text-gray-500 hover:text-gray-700 disabled:opacity-50"
                                            title="Re-discover models"
                                        >
                                            <RefreshCw size={18} className={rediscoveringId === p.id ? 'animate-spin' : ''} />
                                        </button>
                                        <button
                                            onClick={() => handleToggleEnabled(p)}
                                            disabled={togglingId === p.id}
                                            className={`disabled:opacity-50 ${p.enabled ? 'text-green-600 hover:text-green-800' : 'text-gray-400 hover:text-gray-600'}`}
                                            title={p.enabled ? 'Deactivate (pause)' : 'Activate (resume)'}
                                        >
                                            {p.enabled ? <Pause size={18} /> : <Play size={18} />}
                                        </button>
                                    </>
                                ) : (
                                    <>
                                        <button onClick={() => handleOpenModal(p)} className="text-gray-500 hover:text-gray-700" title="Edit">
                                            <Edit2 size={18} />
                                        </button>
                                        <button onClick={() => handleDelete(p.id)} className="text-red-500 hover:text-red-700" title="Delete">
                                            <Trash2 size={18} />
                                        </button>
                                    </>
                                )}
                            </div>
                        </div>
                        <p className="text-sm text-gray-600 mb-1 truncate"><span className="font-semibold">URL:</span> {p.base_url}</p>
                        {p.default_model && <p className="text-sm text-gray-600 mb-1"><span className="font-semibold">Default Model:</span> {p.default_model}</p>}
                        {p.supported_models && <p className="text-xs text-gray-500 mt-2 break-words"><span className="font-semibold">Models:</span> {p.supported_models.split(',').join(', ')}</p>}
                        {p.builtin && (
                            <div className="mt-3">
                                {!p.api_key && (
                                    <p className="text-xs text-amber-600 mb-2">Free to use — activate with a free API key to enable this provider.</p>
                                )}
                                <button
                                    onClick={() => openActivateModal(p)}
                                    className={`w-full py-2 px-4 rounded-md font-semibold text-sm transition-colors ${
                                        p.api_key
                                            ? 'bg-gray-100 text-gray-700 hover:bg-gray-200'
                                            : 'bg-indigo-600 text-white hover:bg-indigo-700 shadow-sm'
                                    }`}
                                >
                                    <span className="flex items-center justify-center gap-2">
                                        <KeyRound size={16} />
                                        {p.api_key ? 'Update API Key' : 'Activate'}
                                    </span>
                                </button>
                            </div>
                        )}
                    </div>
                ))}
            </div>

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

            {activateProvider && (
                <div className="fixed inset-0 bg-black/50 flex justify-center items-center z-50">
                    <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-lg max-h-[90vh] flex flex-col">
                        <h2 className="text-xl font-bold mb-1">
                            {activateProvider.api_key ? 'Update' : 'Activate'} {activateProvider.name}
                        </h2>
                        <p className="text-sm text-gray-500 mb-4">{activateProvider.base_url}</p>

                        <div className="space-y-4 overflow-y-auto flex-1 pr-2">
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">API Key</label>
                                <input
                                    type="password"
                                    autoFocus
                                    value={activateApiKey}
                                    onChange={e => setActivateApiKey(e.target.value)}
                                    placeholder="Paste your free API key"
                                    className="w-full border rounded p-2"
                                />
                            </div>

                            <button
                                type="button"
                                onClick={handleTestActivate}
                                disabled={isActivateTesting || !activateApiKey}
                                className="w-full bg-gray-100 text-gray-800 py-2 px-4 rounded-md border font-medium hover:bg-gray-200 disabled:opacity-50"
                            >
                                {isActivateTesting ? 'Testing...' : 'Test Connection'}
                            </button>

                            {activateTestResult?.status === 'ok' && (
                                <p className="text-green-600 text-sm font-semibold">Connection successful! {activateProvider.name} is ready to use.</p>
                            )}
                            {activateTestResult && activateTestResult.status !== 'ok' && (
                                <p className="text-red-600 text-sm font-semibold whitespace-pre-wrap">{activateTestResult.error}</p>
                            )}

                            <div className="border-t pt-4">
                                <div className="flex justify-between items-center mb-2">
                                    <label className="block text-sm font-medium text-gray-700">Models</label>
                                    <button
                                        type="button"
                                        onClick={() => handleRediscover(activateProvider)}
                                        disabled={rediscoveringId === activateProvider.id}
                                        className="text-indigo-600 hover:text-indigo-800 text-xs font-medium flex items-center gap-1 disabled:opacity-50"
                                    >
                                        <RefreshCw size={14} className={rediscoveringId === activateProvider.id ? 'animate-spin' : ''} />
                                        Re-discover Models
                                    </button>
                                </div>
                                <p className="text-xs text-gray-500">Model lists are discovered automatically and can't be edited by hand.</p>
                                {activateProvider.default_model && (
                                    <p className="text-xs text-gray-600 mt-2"><span className="font-semibold">Default:</span> {activateProvider.default_model}</p>
                                )}
                                {activateProvider.supported_models && (
                                    <p className="text-xs text-gray-500 mt-1 break-words">{activateProvider.supported_models.split(',').join(', ')}</p>
                                )}
                            </div>
                        </div>

                        <div className="flex justify-end space-x-3 pt-4 border-t mt-4">
                            <button type="button" onClick={closeActivateModal} className="text-gray-500 hover:text-gray-700 px-4 py-2">Cancel</button>
                            <button
                                type="button"
                                onClick={handleSaveActivate}
                                disabled={isActivateSaving || activateTestResult?.status !== 'ok'}
                                className="bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 flex items-center disabled:opacity-50 disabled:cursor-not-allowed"
                            >
                                {isActivateSaving ? 'Saving...' : 'Save'}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};
