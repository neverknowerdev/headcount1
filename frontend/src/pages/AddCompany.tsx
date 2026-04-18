/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useStore } from '../store';
import { CirclePicker } from 'react-color';

// Predefined popular vibrant colors
const presetColors = ['#4f46e5', '#f44336', '#e91e63', '#9c27b0', '#673ab7', '#3f51b5', '#2196f3', '#03a9f4', '#00bcd4', '#009688', '#4caf50', '#8bc34a', '#cddc39', '#ffeb3b', '#ffc107', '#ff9800', '#ff5722', '#795548', '#607d8b'];

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
    const [providerUrl, setProviderUrl] = useState('https://api.openai.com/');
    const [providerKey, setProviderKey] = useState('');
    const [providerModel, setProviderModel] = useState('gpt-4');
    const [isTesting, setIsTesting] = useState(false);
    const [testResult, setTestResult] = useState<string | null>(null);
    const [testLog, setTestLog] = useState<string | null>(null);
    const [showLog, setShowLog] = useState(false);
    const [providerType, setProviderType] = useState<string | null>(null);
    const [resolvedUrl, setResolvedUrl] = useState<string | null>(null);

    // Existing Providers (for step 2 if !isInitialOnboarding)
    const [existingProviders, setExistingProviders] = useState<any[]>([]);
    const [selectedExistingProviderId, setSelectedExistingProviderId] = useState<string>('');

    // Step 3: CEO
    const [ceoName, setCeoName] = useState('CEO Agent');
    const [ceoPrompt, setCeoPrompt] = useState('');

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

                if (parsed.providerUrl) setProviderUrl(parsed.providerUrl);
                if (parsed.providerKey) setProviderKey(parsed.providerKey);
                if (parsed.providerModel) setProviderModel(parsed.providerModel);
                if (parsed.selectedExistingProviderId) setSelectedExistingProviderId(parsed.selectedExistingProviderId);

                if (parsed.ceoName) setCeoName(parsed.ceoName);
                if (parsed.ceoPrompt) setCeoPrompt(parsed.ceoPrompt);
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
            }).catch(console.error);
        }
    }, [isInitialOnboarding]);

    // Save to LocalStorage whenever state changes
    useEffect(() => {
        const stateToSave = {
            step, name, shortName, color, providerUrl, providerKey, providerModel, selectedExistingProviderId, ceoName, ceoPrompt
        };
        localStorage.setItem(LS_KEY, JSON.stringify(stateToSave));
    }, [step, name, shortName, color, providerUrl, providerKey, providerModel, selectedExistingProviderId, ceoName, ceoPrompt]);


    useEffect(() => {
        if (name && !ceoPrompt) {
            setCeoPrompt(`Your CEO of ${name}. Your goal is to keep an eye on tasks, delegate work to other agents, keep eye on their work, escalate to human if needed, and do whatever we need to achieve company goals`);
        }
    }, [name, ceoPrompt]);

    const handleCompanyNext = (e: React.FormEvent) => {
        e.preventDefault();
        setStep(2);
    };

    const handleTestProvider = async () => {
        setIsTesting(true);
        setTestResult(null);
        setTestLog(null);
        setShowLog(false);
        setProviderType(null);
        setResolvedUrl(null);
        try {
            const res = await axios.post('/api/providers/test', {
                base_url: providerUrl,
                api_key: providerKey,
                model: providerModel
            });
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

            if (isInitialOnboarding) {
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
            window.location.href = '/';
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
                        <div className="rounded-md shadow-sm -space-y-px flex flex-col gap-4">
                            <div>
                                <label className="text-sm font-medium text-gray-700">Company Name</label>
                                <input required type="text" value={name} onChange={e => setName(e.target.value)} className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300 placeholder-gray-500 text-gray-900 focus:outline-none focus:ring-indigo-500 focus:border-indigo-500 focus:z-10 sm:text-sm" placeholder="Acme Corp" />
                            </div>
                            <div>
                                <label className="text-sm font-medium text-gray-700">Short Name (for folders)</label>
                                <input required type="text" value={shortName} onChange={e => setShortName(e.target.value)} className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300 placeholder-gray-500 text-gray-900 focus:outline-none focus:ring-indigo-500 focus:border-indigo-500 focus:z-10 sm:text-sm" placeholder="acme" />
                            </div>
                            <div>
                                <label className="text-sm font-medium text-gray-700 mb-2 block">Workspace Color</label>
                                <div className="mb-4">
                                  <CirclePicker
                                      color={color}
                                      onChangeComplete={(c: any) => setColor(c.hex)}
                                      colors={presetColors}
                                      width="100%"
                                  />
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
                        <div className="rounded-md shadow-sm -space-y-px flex flex-col gap-4">
                            <div>
                                <label className="text-sm font-medium text-gray-700">OpenAI/Anthropic Compatible URL</label>
                                <input required type="text" value={providerUrl} onChange={e => setProviderUrl(e.target.value)} className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
                            </div>
                            <div>
                                <label className="text-sm font-medium text-gray-700">API Key</label>
                                <input required type="password" value={providerKey} onChange={e => setProviderKey(e.target.value)} className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
                            </div>
                            <div>
                                <label className="text-sm font-medium text-gray-700">Model Name</label>
                                <input required type="text" value={providerModel} onChange={e => setProviderModel(e.target.value)} className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
                            </div>

                            <button type="button" onClick={handleTestProvider} disabled={isTesting} className="w-full bg-gray-100 text-gray-800 py-2 px-4 rounded-md border font-medium hover:bg-gray-200">
                                {isTesting ? 'Testing...' : 'Test Connection'}
                            </button>

                            {testResult === 'success' && <p className="text-green-600 text-sm font-semibold">Connection successful! ({providerType || 'unknown'} detected)</p>}
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
                        <div className="rounded-md shadow-sm -space-y-px flex flex-col gap-4">
                            <p className="text-sm text-gray-600 mb-2">
                                Please select an existing LLM Provider to use for this workspace. You can add new providers later in Settings.
                            </p>
                            <div>
                                <label className="text-sm font-medium text-gray-700">Select Provider</label>
                                <select
                                    className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300"
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
                                    className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300"
                                />
                            </div>
                        </div>
                        <button type="submit" className="group relative w-full flex justify-center py-2 px-4 border border-transparent text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700">
                            Next Step
                        </button>
                    </form>
                )}

                {step === 3 && (
                    <form className="mt-8 space-y-6" onSubmit={handleFinish}>
                        <div className="rounded-md shadow-sm -space-y-px flex flex-col gap-4">
                            <div>
                                <label className="text-sm font-medium text-gray-700">Agent Name</label>
                                <input required type="text" value={ceoName} onChange={e => setCeoName(e.target.value)} className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
                            </div>
                            <div>
                                <label className="text-sm font-medium text-gray-700">System Prompt</label>
                                <textarea required rows={5} value={ceoPrompt} onChange={e => setCeoPrompt(e.target.value)} className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
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
