/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useStore } from '../store';


export const Onboarding: React.FC = () => {
    const { companies } = useStore();
    const isAddCompanyFlow = companies.length > 0;

    const [step, setStep] = useState(1);

    const [existingProviders, setExistingProviders] = useState<any[]>([]);

    useEffect(() => {
        if (isAddCompanyFlow) {
            axios.get('/api/providers').then(res => {
                setExistingProviders(res.data || []);
            }).catch(console.error);
        }
    }, [isAddCompanyFlow]);

    // Step 1: Company
    const [name, setName] = useState('');
    const [shortName, setShortName] = useState('');
    const [color, setColor] = useState('#4f46e5');
    // const [createdCompanyId, setCreatedCompanyId] = useState<number | null>(null);

    // Step 2: Provider
    const [providerUrl, setProviderUrl] = useState('https://api.openai.com/');
    const [providerKey, setProviderKey] = useState('');
    const [providerModel, setProviderModel] = useState('gpt-4');
    const [isTesting, setIsTesting] = useState(false);
    const [testResult, setTestResult] = useState<string | null>(null);
    const [testLog, setTestLog] = useState<string | null>(null);
    const [showLog, setShowLog] = useState(false);
    const [providerType, setProviderType] = useState<string | null>(null);


    // Step 3: CEO
    const [ceoName, setCeoName] = useState('CEO Agent');
    const [ceoPrompt, setCeoPrompt] = useState('');



    useEffect(() => {
        if (name) {
            setCeoPrompt(`Your CEO of ${name}. Your goal is to keep an eye on tasks, delegate work to other agents, keep eye on their work, escalate to human if needed, and do whatever we need to achieve company goals`);
        }
    }, [name]);

    const handleCreateCompany = async (e: React.FormEvent) => {
        e.preventDefault();
        // Just move to the next step, create company at the very end

        // Ensure default selection for provider if it's the add flow
        if (isAddCompanyFlow && existingProviders.length > 0) {
            setResolvedUrl(existingProviders[0].base_url);
        }

        setStep(2);
    };

    const [resolvedUrl, setResolvedUrl] = useState<string | null>(null);

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

    const handleCreateProvider = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!isAddCompanyFlow) {
            try {
                await axios.post('/api/providers', {
                    name: 'Main Provider',
                    base_url: resolvedUrl || providerUrl,
                    api_key: providerKey
                });
                setStep(3);
            } catch {
                alert('Failed to save provider');
            }
        } else {
            setStep(3);
        }
    };

    const handleCreateCEO = async (e: React.FormEvent) => {
        e.preventDefault();
        try {
            // 1. Create company first
            const compRes = await axios.post('/api/companies', { name, short_name: shortName, color });
            const newCompanyId = compRes.data.id;

            // 2. Create CEO
            await axios.post('/api/agents', {
                company_id: newCompanyId,
                name: ceoName,
                description: 'Company CEO',
                system_prompt: ceoPrompt,
                model: providerModel
            });
            window.location.href = '/';
            setTimeout(() => window.location.reload(), 100); // Reload to fetch companies and start app
        } catch {
            alert('Failed to create company or CEO agent');
        }
    };

    return (
        <div className="min-h-screen flex items-center justify-center bg-gray-50 py-12 px-4 sm:px-6 lg:px-8">
            <div className="max-w-md w-full space-y-8 bg-white p-8 rounded-xl shadow-lg">
                <div>
                    <h2 className="mt-6 text-center text-3xl font-extrabold text-gray-900">
                        {step === 1 && "Create a Workspace"}
                        {step === 2 && "Setup LLM Provider"}
                        {step === 3 && "Hire your CEO"}
                    </h2>
                </div>

                {step === 1 && (
                    <form className="mt-8 space-y-6" onSubmit={handleCreateCompany}>
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
                                <label className="text-sm font-medium text-gray-700">Workspace Color</label>
                                <input required type="color" value={color} onChange={e => setColor(e.target.value)} className="h-10 w-full" />
                            </div>
                        </div>
                        <button type="submit" className="group relative w-full flex justify-center py-2 px-4 border border-transparent text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-indigo-500">
                            Next Step
                        </button>
                    </form>
                )}

                {step === 2 && (
                    <form className="mt-8 space-y-6" onSubmit={handleCreateProvider}>
                        <div className="rounded-md shadow-sm -space-y-px flex flex-col gap-4">
                            {!isAddCompanyFlow ? (
                                <>
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
                                </>
                            ) : (
                                <>
                                    <div>
                                        <label className="text-sm font-medium text-gray-700">Select Provider</label>
                                        <select className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" required onChange={(e) => {
                                            const p = existingProviders.find(prov => prov.id === Number(e.target.value));
                                            if (p) setResolvedUrl(p.base_url);
                                        }}>
                                            {existingProviders.map((p, idx) => (
                                                <option key={idx} value={p.id}>{p.name} ({p.base_url})</option>
                                            ))}
                                        </select>
                                    </div>
                                    <div>
                                        <label className="text-sm font-medium text-gray-700">Model Name</label>
                                        <input required type="text" value={providerModel} onChange={e => setProviderModel(e.target.value)} className="appearance-none rounded-md relative block w-full px-3 py-2 border border-gray-300" />
                                    </div>
                                    <p className="text-sm text-gray-500 mt-2">You can add more providers later in Settings.</p>
                                </>
                            )}
                        </div>
                        <button type="submit" disabled={!isAddCompanyFlow && testResult !== 'success'} className="group relative w-full flex justify-center py-2 px-4 border border-transparent text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700 disabled:bg-indigo-300">
                            Next Step
                        </button>
                    </form>
                )}

                {step === 3 && (
                    <form className="mt-8 space-y-6" onSubmit={handleCreateCEO}>
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
