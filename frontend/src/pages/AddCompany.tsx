/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useStore } from '../store';

// Predefined popular vibrant colors
const presetColors = ['#4f46e5', '#f44336', '#e91e63', '#9c27b0', '#673ab7', '#3f51b5', '#2196f3', '#03a9f4', '#00bcd4', '#009688', '#4caf50', '#8bc34a', '#cddc39', '#ffeb3b', '#ffc107', '#ff9800', '#ff5722', '#795548'];

// Copy + "get an API key" links for the builtin free-model providers seeded on
// startup (see db.ProviderNameOpenRouter / db.ProviderNameOpenCodeZen). Keyed
// by the provider's DB name so this stays in sync automatically if the list
// of builtin providers ever grows.
const FREE_PROVIDER_INFO: Record<string, { blurb: string; keyUrl: string; keyUrlLabel: string }> = {
    'OpenRouter Free Models': {
        blurb: 'A gateway to dozens of free community models — no credit card required.',
        keyUrl: 'https://openrouter.ai/keys',
        keyUrlLabel: 'openrouter.ai/keys',
    },
    'OpenCode Free Models': {
        blurb: 'Free models curated for coding agents — no credit card required.',
        keyUrl: 'https://opencode.ai/auth',
        keyUrlLabel: 'opencode.ai/auth',
    },
};

export const AddCompany: React.FC = () => {
    const { companies } = useStore();
    const isInitialOnboarding = companies.length === 0;

    // LocalStorage keys
    const LS_KEY = 'addCompanyState';

    // Step state
    const [step, setStep] = useState(1);

    // Step 1: Company
    const [name, setName] = useState('');
    const [shortName, setShortName] = useState('');
    const [color, setColor] = useState('#4f46e5');

    // Step 2: Provider
    // providerMode picks between the default "free" path (builtin OpenRouter /
    // OpenCode Zen providers) and "custom" (the original from-scratch form).
    const [providerMode, setProviderMode] = useState<'free' | 'custom'>('free');
    const [providerUrl, setProviderUrl] = useState('https://api.openai.com/');
    const [providerKey, setProviderKey] = useState('');
    const [providerModel, setProviderModel] = useState('gpt-4');
    const [isTesting, setIsTesting] = useState(false);
    const [testResult, setTestResult] = useState<string | null>(null);
    const [testLog, setTestLog] = useState<string | null>(null);
    const [showLog, setShowLog] = useState(false);
    const [providerType, setProviderType] = useState<string | null>(null);
    const [resolvedUrl, setResolvedUrl] = useState<string | null>(null);

    // Free builtin providers (OpenRouter / OpenCode Zen), fetched on mount.
    const [builtinProviders, setBuiltinProviders] = useState<any[]>([]);
    const [builtinProvidersLoaded, setBuiltinProvidersLoaded] = useState(false);
    const [freeProviderName, setFreeProviderName] = useState<string>('OpenRouter Free Models');
    const [freeApiKey, setFreeApiKey] = useState('');

    // Existing Providers (for step 2 if !isInitialOnboarding)
    const [existingProviders, setExistingProviders] = useState<any[]>([]);
    const [existingProvidersLoaded, setExistingProvidersLoaded] = useState(false);
    const [selectedExistingProviderId, setSelectedExistingProviderId] = useState<string>('');

    // Step 3: CEO
    const [ceoName, setCeoName] = useState('CEO Agent');
    const [ceoPrompt, setCeoPrompt] = useState('');
    const [hasManuallyEditedPrompt, setHasManuallyEditedPrompt] = useState(false);

    // Load from LocalStorage on mount
    useEffect(() => {
        const saved = localStorage.getItem(LS_KEY);
        if (saved) {
            try {
                const parsed = JSON.parse(saved);
                if (parsed.step) setStep(parsed.step);
                if (parsed.name) setName(parsed.name);
                if (parsed.shortName) setShortName(parsed.shortName);
                if (parsed.color) setColor(parsed.color);

                if (parsed.providerMode) setProviderMode(parsed.providerMode);
                if (parsed.freeProviderName) setFreeProviderName(parsed.freeProviderName);
                if (parsed.providerUrl) setProviderUrl(parsed.providerUrl);
                if (parsed.providerKey) setProviderKey(parsed.providerKey);
                if (parsed.providerModel) setProviderModel(parsed.providerModel);
                if (parsed.selectedExistingProviderId) setSelectedExistingProviderId(parsed.selectedExistingProviderId);

                if (parsed.ceoName) setCeoName(parsed.ceoName);
                if (parsed.ceoPrompt) setCeoPrompt(parsed.ceoPrompt);
                if (parsed.hasManuallyEditedPrompt) setHasManuallyEditedPrompt(parsed.hasManuallyEditedPrompt);
            } catch (e) {
                console.error("Failed to load saved state", e);
            }
        }

        if (!isInitialOnboarding) {
            // Fetch existing providers
            axios.get('/api/providers').then(res => {
                if (res.data && res.data.length > 0) {
                    setExistingProviders(res.data);
                    setSelectedExistingProviderId(res.data[0].id.toString());
                    setProviderModel(res.data[0].default_model);
                }
            }).catch(console.error).finally(() => setExistingProvidersLoaded(true));
        } else {
            // Fetch the builtin free-model providers (OpenRouter, OpenCode Zen)
            // seeded automatically on server startup, so the default onboarding
            // path can offer them instead of a from-scratch provider form.
            axios.get('/api/providers').then(res => {
                const builtins = (res.data || []).filter((p: any) => p.builtin);
                setBuiltinProviders(builtins);
                if (builtins.length > 0) {
                    setFreeProviderName(prev => builtins.some((p: any) => p.name === prev) ? prev : builtins[0].name);
                } else {
                    // No builtin providers available (e.g. an older server) —
                    // go straight to the custom provider form.
                    setProviderMode('custom');
                }
            }).catch(() => setProviderMode('custom')).finally(() => setBuiltinProvidersLoaded(true));
        }
    }, [isInitialOnboarding]);

    // Save to LocalStorage whenever state changes. The free-provider API key
    // is intentionally excluded — it's only needed transiently to test+save
    // the builtin provider row, not worth persisting in plaintext.
    useEffect(() => {
        const stateToSave = {
            step, name, shortName, color, providerMode, freeProviderName, providerUrl, providerKey, providerModel, selectedExistingProviderId, ceoName, ceoPrompt, hasManuallyEditedPrompt
        };
        localStorage.setItem(LS_KEY, JSON.stringify(stateToSave));
    }, [step, name, shortName, color, providerMode, freeProviderName, providerUrl, providerKey, providerModel, selectedExistingProviderId, ceoName, ceoPrompt, hasManuallyEditedPrompt]);


    useEffect(() => {
        if (name && !hasManuallyEditedPrompt) {
            setCeoPrompt(`Your CEO of ${name}. Your goal is to keep an eye on tasks, delegate work to other agents, keep eye on their work, escalate to human if needed, and do whatever we need to achieve company goals`);
        }
    }, [name, hasManuallyEditedPrompt]);

    const handleCompanyNext = (e: React.FormEvent) => {
        e.preventDefault();
        setStep(2);
    };

    // runProviderTest POSTs to /api/providers/test and updates the shared
    // test-result state. Used by both the custom-provider form and the
    // free-provider (OpenRouter/OpenCode Zen) picker.
    const runProviderTest = async (payload: Record<string, unknown>) => {
        setIsTesting(true);
        setTestResult(null);
        setTestLog(null);
        setShowLog(false);
        setProviderType(null);
        setResolvedUrl(null);
        try {
            const res = await axios.post('/api/providers/test', payload);
            setTestResult('success');
            if (res.data) {
                if (res.data.log) setTestLog(res.data.log);
                if (res.data.provider_type) setProviderType(res.data.provider_type);
                if (res.data.url) setResolvedUrl(res.data.url);
            }
        } catch (error: any) {
            if (error.response && error.response.data) {
                if (error.response.data.error) setTestResult(error.response.data.error);
                else setTestResult('Connection failed. Check details and try again.');

                if (error.response.data.log) setTestLog(error.response.data.log);
            } else if (error.message) {
                setTestResult(error.message);
                setTestLog(error.message);
            } else {
                setTestResult('Connection failed. Check details and try again.');
            }
        } finally {
            setIsTesting(false);
        }
    };

    const handleTestProvider = () => runProviderTest({
        base_url: providerUrl,
        api_key: providerKey,
        model: providerModel,
    });

    const selectedFreeProvider = builtinProviders.find(p => p.name === freeProviderName) || null;

    const handleTestFreeProvider = () => {
        if (!selectedFreeProvider) return;
        return runProviderTest({
            provider_id: selectedFreeProvider.id,
            base_url: selectedFreeProvider.base_url,
            api_key: freeApiKey,
            model: selectedFreeProvider.default_model,
            provider_type: selectedFreeProvider.provider_type,
        });
    };

    // Switching the provider mode or the selected free provider invalidates
    // whatever test result is currently shown.
    const switchProviderMode = (mode: 'free' | 'custom') => {
        setProviderMode(mode);
        setTestResult(null);
        setTestLog(null);
        setShowLog(false);
    };

    const selectFreeProvider = (providerName: string) => {
        setFreeProviderName(providerName);
        setTestResult(null);
        setTestLog(null);
        setShowLog(false);
    };

    const handleProviderNext = (e: React.FormEvent) => {
        e.preventDefault();
        setStep(3);
    };

    const handleExistingProviderNext = (e: React.FormEvent) => {
        e.preventDefault();
        setStep(3);
    }

    const handleFinish = async (e: React.FormEvent) => {
        e.preventDefault();
        try {
            let finalProviderId: number | null = null;
            let finalProviderModel = providerModel;

            if (isInitialOnboarding && providerMode === 'free' && selectedFreeProvider) {
                // Reuse the builtin provider row seeded on startup — just fill
                // in the API key the user just verified, preserving its
                // discovered model catalog (name/base_url/default_model/
                // supported_models) rather than overwriting it. Deliberately
                // ignore the test probe's auto-detected url/provider_type: these
                // builtin providers are known OpenAI-compatible gateways, and
                // the probe races an OpenAI- and an Anthropic-shaped request
                // concurrently — whichever happens to respond first "wins",
                // which could otherwise clobber base_url with the wrong shape.
                await axios.put(`/api/providers/${selectedFreeProvider.id}`, {
                    name: selectedFreeProvider.name,
                    base_url: selectedFreeProvider.base_url,
                    api_key: freeApiKey,
                    provider_type: selectedFreeProvider.provider_type,
                    default_model: selectedFreeProvider.default_model,
                    supported_models: selectedFreeProvider.supported_models,
                });
                finalProviderId = selectedFreeProvider.id;
                finalProviderModel = selectedFreeProvider.default_model;
            } else if (isInitialOnboarding) {
                // 1. Create Provider
                const providerRes = await axios.post('/api/providers', {
                    name: 'Main Provider',
                    base_url: resolvedUrl || providerUrl,
                    api_key: providerKey,
                    provider_type: providerType,
                    default_model: providerModel,
                    supported_models: providerModel
                });
                finalProviderId = providerRes.data.id;
            } else {
                finalProviderId = parseInt(selectedExistingProviderId);
                const selectedProv = existingProviders.find(p => p.id === finalProviderId);
                if (selectedProv && providerModel === selectedProv.default_model) {
                   // User didn't change model manually, use default
                   finalProviderModel = selectedProv.default_model;
                }
            }

            // 2. Create Company
            const companyRes = await axios.post('/api/companies', { name, short_name: shortName, color });
            const finalCompanyId = companyRes.data.id;

            // 3. Create CEO Agent
            await axios.post('/api/agents', {
                company_id: finalCompanyId,
                name: ceoName,
                description: 'Company CEO',
                system_prompt: ceoPrompt,
                model: finalProviderModel,
                provider_id: finalProviderId
            });

            // Success! Clear localstorage and redirect
            localStorage.removeItem(LS_KEY);
            window.location.href = `/companies/${companyRes.data.short_name}`;
        } catch (err) {
            console.error(err);
            alert('Failed to complete setup. Check console for details.');
        }
    };

    return (
        <div className="min-h-screen flex items-center justify-center bg-gray-50 py-12 px-4 sm:px-6 lg:px-8">
            <div className="max-w-md w-full space-y-8 bg-white p-8 rounded-xl shadow-lg">
                <div>
                    <h2 className="mt-6 text-center text-3xl font-extrabold text-gray-900">
                        {step === 1 && (isInitialOnboarding ? "Create a Workspace" : "Add Workspace")}
                        {step === 2 && "Setup LLM Provider"}
                        {step === 3 && "Hire your CEO"}
                    </h2>
                </div>

                {step === 1 && (
                    <form className="mt-8 space-y-6" onSubmit={handleCompanyNext}>
                        <div className="flex flex-col gap-4">
                            <div>
                                <label className="text-sm font-medium text-gray-700">Company Name</label>
                                <input required type="text" value={name} onChange={e => setName(e.target.value)} className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300 placeholder-gray-500 text-gray-900 focus:outline-none focus:ring-indigo-500 focus:border-indigo-500 focus:z-10 sm:text-sm" placeholder="Acme Corp" />
                            </div>
                            <div>
                                <label className="text-sm font-medium text-gray-700">Short Name (for folders)</label>
                                <input required type="text" value={shortName} onChange={e => setShortName(e.target.value)} className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300 placeholder-gray-500 text-gray-900 focus:outline-none focus:ring-indigo-500 focus:border-indigo-500 focus:z-10 sm:text-sm" placeholder="acme" />
                            </div>
                            <div>
                                <label className="text-sm font-medium text-gray-700 mb-2 block">Workspace Color</label>
                                <div className="flex flex-wrap gap-3 mb-4">
                                    {presetColors.map(c => (
                                        <button
                                            key={c}
                                            type="button"
                                            onClick={() => setColor(c)}
                                            className={`w-8 h-8 rounded-full cursor-pointer transition-transform hover:scale-110 focus:outline-none`}
                                            style={{ backgroundColor: c, boxShadow: color === c ? `0 0 0 2px white, 0 0 0 4px ${c}` : 'none' }}
                                            title={c}
                                        />
                                    ))}
                                </div>
                                <div className="flex items-center gap-2">
                                  <span className="text-xs text-gray-500">Custom:</span>
                                  <input required type="color" value={color} onChange={e => setColor(e.target.value)} className="h-8 w-16 p-0 border-0 rounded cursor-pointer" />
                                </div>
                            </div>
                        </div>
                        <button type="submit" className="group relative w-full flex justify-center py-2 px-4 border border-transparent text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-indigo-500">
                            Next Step
                        </button>
                    </form>
                )}

                {step === 2 && isInitialOnboarding && (
                    <form className="mt-8 space-y-6" onSubmit={handleProviderNext}>
                        <p className="text-xs text-gray-500 -mt-4">
                            You can add, edit, or switch providers anytime later from <span className="font-medium text-gray-600">Settings → LLM Providers</span>.
                        </p>

                        <div className="flex flex-col gap-4">
                            {providerMode === 'free' ? (
                                <>
                                    {!builtinProvidersLoaded ? (
                                        <p className="text-sm text-gray-400">Loading available free providers…</p>
                                    ) : (
                                        <>
                                            <div className="flex flex-col gap-2">
                                                {builtinProviders.map(p => {
                                                    const info = FREE_PROVIDER_INFO[p.name] || { blurb: 'Free models.', keyUrl: '#', keyUrlLabel: '' };
                                                    const selected = p.name === freeProviderName;
                                                    return (
                                                        <button
                                                            key={p.id}
                                                            type="button"
                                                            onClick={() => selectFreeProvider(p.name)}
                                                            className={`text-left p-3 rounded-md border transition-colors ${selected ? 'border-indigo-600 bg-indigo-50 ring-1 ring-indigo-200' : 'border-gray-300 hover:border-gray-400'}`}
                                                        >
                                                            <div className="flex items-center justify-between">
                                                                <span className="font-semibold text-gray-900">{p.name}</span>
                                                                <span className="text-xs font-medium bg-green-100 text-green-700 px-2 py-0.5 rounded-full">Free</span>
                                                            </div>
                                                            <p className="text-xs text-gray-500 mt-1">{info.blurb}</p>
                                                        </button>
                                                    );
                                                })}
                                            </div>

                                            {selectedFreeProvider && (
                                                <>
                                                    <p className="text-xs text-gray-500">
                                                        Get a free API key at{' '}
                                                        <a
                                                            href={FREE_PROVIDER_INFO[selectedFreeProvider.name]?.keyUrl || '#'}
                                                            target="_blank"
                                                            rel="noreferrer"
                                                            className="text-indigo-600 underline font-medium"
                                                        >
                                                            {FREE_PROVIDER_INFO[selectedFreeProvider.name]?.keyUrlLabel || selectedFreeProvider.base_url}
                                                        </a>
                                                        {' '}— no credit card required.
                                                    </p>
                                                    <div>
                                                        <label className="text-sm font-medium text-gray-700">{selectedFreeProvider.name} API Key</label>
                                                        <input
                                                            required
                                                            type="password"
                                                            value={freeApiKey}
                                                            onChange={e => setFreeApiKey(e.target.value)}
                                                            className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300"
                                                            placeholder="Paste your API key"
                                                        />
                                                    </div>
                                                </>
                                            )}

                                            <button type="button" onClick={() => switchProviderMode('custom')} className="text-xs text-gray-500 underline hover:text-gray-700 text-center">
                                                Use a custom provider instead
                                            </button>
                                        </>
                                    )}
                                </>
                            ) : (
                                <>
                                    {builtinProviders.length > 0 && (
                                        <button type="button" onClick={() => switchProviderMode('free')} className="text-xs text-gray-500 underline hover:text-gray-700 self-start">
                                            ← Back to free providers
                                        </button>
                                    )}
                                    <div>
                                        <label className="text-sm font-medium text-gray-700">OpenAI/Anthropic Compatible URL</label>
                                        <input required type="text" value={providerUrl} onChange={e => setProviderUrl(e.target.value)} className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
                                    </div>
                                    <div>
                                        <label className="text-sm font-medium text-gray-700">API Key</label>
                                        <input required type="password" value={providerKey} onChange={e => setProviderKey(e.target.value)} className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
                                    </div>
                                    <div>
                                        <label className="text-sm font-medium text-gray-700">Model Name</label>
                                        <input required type="text" value={providerModel} onChange={e => setProviderModel(e.target.value)} className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
                                    </div>
                                </>
                            )}

                            {(providerMode === 'custom' || selectedFreeProvider) && (
                                <button
                                    type="button"
                                    onClick={providerMode === 'free' ? handleTestFreeProvider : handleTestProvider}
                                    disabled={isTesting || (providerMode === 'free' && !freeApiKey)}
                                    className="w-full bg-gray-100 text-gray-800 py-2 px-4 rounded-md border font-medium hover:bg-gray-200 disabled:opacity-50"
                                >
                                    {isTesting ? 'Testing...' : 'Test Connection'}
                                </button>
                            )}

                            {testResult === 'success' && (
                                <p className="text-green-600 text-sm font-semibold">
                                    {providerMode === 'free' && selectedFreeProvider
                                        ? `Connection successful! ${selectedFreeProvider.name} is ready to use.`
                                        : `Connection successful! (${providerType || 'unknown'} detected)`}
                                </p>
                            )}
                            {testResult && testResult !== 'success' && <p className="text-red-600 text-sm font-semibold whitespace-pre-wrap">{testResult}</p>}

                            {testLog && (
                                <div className="mt-2 border rounded-md overflow-hidden">
                                    <button
                                        type="button"
                                        onClick={() => setShowLog(!showLog)}
                                        className="w-full text-left px-3 py-1.5 bg-gray-50 text-xs font-medium text-gray-600 hover:bg-gray-100 border-b flex justify-between"
                                    >
                                        {showLog ? 'Hide execution log' : 'Show execution log'}
                                        <span>{showLog ? '▲' : '▼'}</span>
                                    </button>
                                    {showLog && (
                                        <pre className="p-3 text-xs bg-gray-900 text-gray-300 overflow-x-auto whitespace-pre-wrap max-h-48">
                                            {testLog}
                                        </pre>
                                    )}
                                </div>
                            )}
                        </div>
                        <button type="submit" disabled={testResult !== 'success'} className="group relative w-full flex justify-center py-2 px-4 border border-transparent text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700 disabled:bg-indigo-300">
                            Next Step
                        </button>
                    </form>
                )}

                {step === 2 && !isInitialOnboarding && (
                    <form className="mt-8 space-y-6" onSubmit={handleExistingProviderNext}>
                        <div className="flex flex-col gap-4">
                            <p className="text-sm text-gray-600 mb-2">
                                Please select an existing LLM Provider to use for this workspace. You can add new providers later in Settings.
                            </p>
                            {!existingProvidersLoaded ? (
                                <p className="text-sm text-gray-400">Loading providers…</p>
                            ) : (
                                <>
                                    <div>
                                        <label className="text-sm font-medium text-gray-700">Select Provider</label>
                                        <select
                                            className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300"
                                            value={selectedExistingProviderId}
                                            onChange={(e) => {
                                                setSelectedExistingProviderId(e.target.value);
                                                const p = existingProviders.find(prov => prov.id.toString() === e.target.value);
                                                if (p) setProviderModel(p.default_model);
                                            }}
                                            required
                                        >
                                            {existingProviders.map(p => (
                                                <option key={p.id} value={p.id}>{p.name} ({p.base_url})</option>
                                            ))}
                                        </select>
                                    </div>
                                    <div>
                                        <label className="text-sm font-medium text-gray-700">Model Name</label>
                                        <input
                                            required
                                            type="text"
                                            value={providerModel}
                                            onChange={e => setProviderModel(e.target.value)}
                                            className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300"
                                        />
                                    </div>
                                </>
                            )}
                        </div>
                        <button type="submit" disabled={!existingProvidersLoaded || existingProviders.length === 0} className="group relative w-full flex justify-center py-2 px-4 border border-transparent text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700 disabled:bg-indigo-300">
                            Next Step
                        </button>
                    </form>
                )}

                {step === 3 && (
                    <form className="mt-8 space-y-6" onSubmit={handleFinish}>
                        <div className="flex flex-col gap-4">
                            <div>
                                <label className="text-sm font-medium text-gray-700">Agent Name</label>
                                <input required type="text" value={ceoName} onChange={e => setCeoName(e.target.value)} className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
                            </div>
                            <div>
                                <label className="text-sm font-medium text-gray-700">System Prompt</label>
                                <textarea required rows={5} value={ceoPrompt} onChange={e => { setCeoPrompt(e.target.value); setHasManuallyEditedPrompt(true); }} className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
                            </div>
                        </div>
                        <button type="submit" className="group relative w-full flex justify-center py-2 px-4 border border-transparent text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700 focus:outline-none">
                            Finish & Launch
                        </button>
                    </form>
                )}
            </div>
        </div>
    );
};
