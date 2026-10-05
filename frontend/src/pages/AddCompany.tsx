import { SecretLabel } from '../components/SecretField';
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
    // The model that actually passed the connection test — may differ from the
    // one requested if the backend fell back off a rate-limited model.
    const [resolvedModel, setResolvedModel] = useState<string | null>(null);
    // After a successful test, the free-provider view lets the user pick which
    // catalog model to save as the default. chosenModelOk gates "Next" — a
    // freshly picked model must pass its own (exact) test before it can be
    // saved, so we never persist a model that's currently rate-limited.
    const [chosenModel, setChosenModel] = useState<string>('');
    const [chosenModelOk, setChosenModelOk] = useState(false);
    const [modelTesting, setModelTesting] = useState(false);
    const [modelTestError, setModelTestError] = useState<string | null>(null);

    // Free builtin providers (OpenRouter / OpenCode Zen), fetched on mount.
    const [builtinProviders, setBuiltinProviders] = useState<any[]>([]);
    const [builtinProvidersLoaded, setBuiltinProvidersLoaded] = useState(false);
    const [freeProviderName, setFreeProviderName] = useState<string>('OpenRouter Free Models');
    const [freeApiKey, setFreeApiKey] = useState('');
    // An already-configured provider (activated + has a default model). When one
    // exists we skip the provider-setup step entirely and just use it.
    const [autoProvider, setAutoProvider] = useState<any | null>(null);


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

                if (parsed.ceoName) setCeoName(parsed.ceoName);
                if (parsed.ceoPrompt) setCeoPrompt(parsed.ceoPrompt);
                if (parsed.hasManuallyEditedPrompt) setHasManuallyEditedPrompt(parsed.hasManuallyEditedPrompt);
            } catch (e) {
                console.error("Failed to load saved state", e);
            }
        }

        // Both the first-run and "Add Workspace" flows use the same provider
        // picker: the builtin free-model providers (OpenRouter, OpenCode Zen)
        // as cards, with a "use a custom provider instead" escape hatch.
        axios.get('/api/providers').then(res => {
            const providers = res.data || [];
            // If a provider is already usable (enabled, has a key, and a default
            // model), skip the provider-setup step and just use it — nothing to
            // configure. Prefer an activated builtin free provider (OpenRouter /
            // OpenCode Zen), which is what this onboarding blesses, over any
            // other configured row; fall back to the first ready one otherwise.
            const ready = providers.filter((p: any) => p.enabled && p.has_api_key && p.default_model);
            const configured = ready.find((p: any) => p.builtin) || ready[0] || null;
            setAutoProvider(configured);
            // Don't strand a restored session on the provider step we now skip.
            if (configured) {
                setStep(s => s === 2 ? 1 : s);
            }

            // Only offer builtin providers the user hasn't deactivated —
            // a provider paused from the LLM Providers page shouldn't be
            // handed to a new company.
            const builtins = providers.filter((p: any) => p.builtin && p.enabled);
            setBuiltinProviders(builtins);
            if (builtins.length > 0) {
                setFreeProviderName(prev => builtins.some((p: any) => p.name === prev) ? prev : builtins[0].name);
            } else {
                // No builtin providers available (e.g. an older server, or
                // all of them deactivated) — go straight to the custom
                // provider form.
                setProviderMode('custom');
            }
        }).catch(() => setProviderMode('custom')).finally(() => setBuiltinProvidersLoaded(true));
    }, [isInitialOnboarding]);

    // Save to LocalStorage whenever state changes. The free-provider API key
    // is intentionally excluded — it's only needed transiently to test+save
    // the builtin provider row, not worth persisting in plaintext.
    useEffect(() => {
        const stateToSave = {
            step, name, shortName, color, providerMode, freeProviderName, providerUrl, providerKey, providerModel, ceoName, ceoPrompt, hasManuallyEditedPrompt
        };
        localStorage.setItem(LS_KEY, JSON.stringify(stateToSave));
    }, [step, name, shortName, color, providerMode, freeProviderName, providerUrl, providerKey, providerModel, ceoName, ceoPrompt, hasManuallyEditedPrompt]);


    useEffect(() => {
        if (name && !hasManuallyEditedPrompt) {
            setCeoPrompt(`Your CEO of ${name}. Your goal is to keep an eye on tasks, delegate work to other agents, keep eye on their work, escalate to human if needed, and do whatever we need to achieve company goals`);
        }
    }, [name, hasManuallyEditedPrompt]);

    const handleCompanyNext = (e: React.FormEvent) => {
        e.preventDefault();
        // Skip provider setup when one is already configured — jump straight to
        // the CEO step and use the existing provider.
        setStep(autoProvider ? 3 : 2);
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
        setResolvedModel(null);
        try {
            const res = await axios.post('/api/providers/test', payload);
            setTestResult('success');
            if (res.data) {
                if (res.data.log) setTestLog(res.data.log);
                if (res.data.provider_type) setProviderType(res.data.provider_type);
                if (res.data.url) setResolvedUrl(res.data.url);
                // The backend may have fallen back to a different (non-rate-
                // limited) model — adopt whichever one actually worked as the
                // pre-selected (and already-verified) default.
                if (res.data.model) {
                    setResolvedModel(res.data.model);
                    setProviderModel(res.data.model);
                    setChosenModel(res.data.model);
                    setChosenModelOk(true);
                    setModelTestError(null);
                }
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

    // A builtin free provider that already has a key saved (activated on an
    // earlier workspace) can be reused without re-entering the key or
    // re-testing. Only when the user types a new key do we require a passing
    // test before continuing.
    const freeProviderPreactivated = !!selectedFreeProvider && selectedFreeProvider.has_api_key && !freeApiKey;
    const providerReady = providerMode === 'free'
        // Free flow also requires the chosen default model to be verified.
        ? (!!selectedFreeProvider && (freeProviderPreactivated || testResult === 'success') && chosenModelOk)
        : testResult === 'success';

    // Models the free provider offers, for the "default model" picker shown
    // after a successful test.
    const freeProviderModels: string[] = (selectedFreeProvider?.supported_models || '')
        .split(',').map((m: string) => m.trim()).filter(Boolean);

    const resetModelChoice = () => {
        setChosenModel('');
        setChosenModelOk(false);
        setModelTesting(false);
        setModelTestError(null);
    };

    const selectFreeProvider = (providerName: string) => {
        setFreeProviderName(providerName);
        setFreeApiKey('');
        setTestResult(null);
        setTestLog(null);
        setShowLog(false);
        resetModelChoice();
    };

    // When the user picks a different default model, verify that specific model
    // (exact — no fallback) before it can be saved. The model that already
    // passed the connection test needs no re-test.
    const chooseFreeModel = async (model: string) => {
        setChosenModel(model);
        setModelTestError(null);
        if (model === resolvedModel) {
            setChosenModelOk(true);
            return;
        }
        if (!selectedFreeProvider) return;
        setChosenModelOk(false);
        setModelTesting(true);
        try {
            await axios.post('/api/providers/test', {
                provider_id: selectedFreeProvider.id,
                base_url: selectedFreeProvider.base_url,
                api_key: freeApiKey,
                model,
                provider_type: selectedFreeProvider.provider_type,
                exact: true,
            });
            setChosenModelOk(true);
        } catch (error: any) {
            setChosenModelOk(false);
            setModelTestError(error?.response?.data?.error || 'This model failed — it may be rate-limited right now. Pick another.');
        } finally {
            setModelTesting(false);
        }
    };

    const handleProviderNext = (e: React.FormEvent) => {
        e.preventDefault();
        setStep(3);
    };

    const handleFinish = async (e: React.FormEvent) => {
        e.preventDefault();
        try {
            let finalProviderId: number | null = null;
            let finalProviderModel = providerModel;

            if (autoProvider) {
                // Provider step was skipped — reuse the already-configured one.
                finalProviderId = autoProvider.id;
                finalProviderModel = autoProvider.default_model;
            } else if (providerMode === 'free' && selectedFreeProvider) {
                // Reuse the builtin provider row (seeded per user) — preserving
                // its discovered model catalog. If the user entered a key,
                // activate/replace it; if the provider was already activated on
                // a previous workspace, reuse it untouched. Deliberately ignore
                // the test probe's auto-detected url/provider_type: these builtin
                // providers are known OpenAI-compatible gateways, and the probe
                // races an OpenAI- and an Anthropic-shaped request concurrently —
                // whichever responds first "wins", which could otherwise clobber
                // base_url with the wrong shape.
                // Save the user's chosen (and verified) default model — falls
                // back to the model the connection test settled on, then the
                // provider's seeded default.
                const workingModel = chosenModel || resolvedModel || selectedFreeProvider.default_model;
                if (freeApiKey) {
                    await axios.put(`/api/providers/${selectedFreeProvider.id}`, {
                        name: selectedFreeProvider.name,
                        base_url: selectedFreeProvider.base_url,
                        api_key: freeApiKey,
                        provider_type: selectedFreeProvider.provider_type,
                        default_model: workingModel,
                        supported_models: selectedFreeProvider.supported_models,
                    });
                }
                finalProviderId = selectedFreeProvider.id;
                finalProviderModel = workingModel;
            } else {
                // Custom provider: create a fresh row from the from-scratch form.
                const providerRes = await axios.post('/api/providers', {
                    name: 'Main Provider',
                    base_url: resolvedUrl || providerUrl,
                    api_key: providerKey,
                    provider_type: providerType,
                    default_model: providerModel,
                    supported_models: providerModel
                });
                finalProviderId = providerRes.data.id;
            }

            // Every task now requires an explicit task-orchestrator model. The
            // onboarding provider is the user's deliberate choice, so make it
            // the initial control-plane default instead of leaving the first
            // task blocked on a setting that is invisible during setup.
            if (finalProviderId && finalProviderModel) {
                await axios.put('/api/default-model-settings/task_orchestrator', {
                    provider_id: finalProviderId,
                    model: finalProviderModel,
                });
            }

            // 2. Create Company
            const companyRes = await axios.post('/api/companies', { name, short_name: shortName, color });
            const finalCompanyId = companyRes.data.id;

            // 3. Create CEO Agent
            await axios.post('/api/agents', {
                company_id: finalCompanyId,
                name: ceoName,
                role_key: 'CEO',
                short_name: 'CEO',
                description: 'Company CEO',
                system_prompt: ceoPrompt,
                model: finalProviderModel,
                provider_id: finalProviderId,
                chat_type: 'compact_thinking',
                reasoning_level: 'max'
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

                {step === 2 && (
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

                                                {/* Escape hatch, styled as a smaller last card. */}
                                                <button
                                                    type="button"
                                                    onClick={() => switchProviderMode('custom')}
                                                    className="text-left px-3 py-2 rounded-md border border-gray-300 hover:border-gray-400 transition-colors flex items-center justify-between"
                                                >
                                                    <span className="text-sm font-medium text-gray-700">Custom provider</span>
                                                    <span className="text-xs text-gray-400">Bring your own endpoint →</span>
                                                </button>
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
                                                        {selectedFreeProvider.has_api_key && (
                                                            <p className="text-xs text-green-600 mt-0.5">
                                                                ✓ Already activated — leave blank to reuse the saved key, or paste a new one to replace it.
                                                            </p>
                                                        )}
                                                        <input
                                                            required={!selectedFreeProvider.has_api_key}
                                                            type="password"
                                                            value={freeApiKey}
                                                            onChange={e => {
                                                                setFreeApiKey(e.target.value);
                                                                // A changed key invalidates the previous test + model choice.
                                                                setTestResult(null);
                                                                resetModelChoice();
                                                            }}
                                                            className="mt-1 appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300"
                                                            placeholder={selectedFreeProvider.has_api_key ? 'Using saved key' : 'Paste your API key'}
                                                        />
                                                    </div>
                                                </>
                                            )}
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
                                        <SecretLabel>API Key</SecretLabel>
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
                                    disabled={isTesting || (providerMode === 'free' && !freeApiKey && !selectedFreeProvider?.has_api_key)}
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

                            {/* After a successful test, let the user pick which model to save as
                                the default. The connection test already settled on a working
                                model (chosenModel); switching to another verifies it first. */}
                            {providerMode === 'free' && testResult === 'success' && freeProviderModels.length > 0 && (
                                <div>
                                    <label className="text-sm font-medium text-gray-700">Default Model</label>
                                    {resolvedModel && selectedFreeProvider && resolvedModel !== selectedFreeProvider.default_model && chosenModel === resolvedModel && (
                                        <p className="text-xs text-gray-500 mt-0.5">
                                            {selectedFreeProvider.default_model} was rate-limited, so {resolvedModel} was picked. Change it below if you like.
                                        </p>
                                    )}
                                    <div className="flex items-center gap-2 mt-1">
                                        <select
                                            value={chosenModel}
                                            onChange={e => chooseFreeModel(e.target.value)}
                                            disabled={modelTesting}
                                            className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300 disabled:opacity-60"
                                        >
                                            {!freeProviderModels.includes(chosenModel) && chosenModel && (
                                                <option value={chosenModel}>{chosenModel}</option>
                                            )}
                                            {freeProviderModels.map((m: string) => (
                                                <option key={m} value={m}>{m}</option>
                                            ))}
                                        </select>
                                        <span className="text-sm whitespace-nowrap">
                                            {modelTesting ? <span className="text-gray-500">Testing…</span>
                                                : chosenModelOk ? <span className="text-green-600 font-semibold">✓ ready</span>
                                                : null}
                                        </span>
                                    </div>
                                    {modelTestError && <p className="text-red-600 text-xs mt-1 whitespace-pre-wrap">{modelTestError}</p>}
                                </div>
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
                        <button type="submit" disabled={!providerReady} className="group relative w-full flex justify-center py-2 px-4 border border-transparent text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700 disabled:bg-indigo-300">
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
