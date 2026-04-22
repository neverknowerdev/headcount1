import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';

import { ArrowLeft, Save } from 'lucide-react';

export const AgentDetails: React.FC = () => {
    const { id, shortName } = useParams<{id: string, shortName: string}>();
    const [agent, setAgent] = useState<any>(null);
    const [stats, setStats] = useState<any>(null);
    const [providers, setProviders] = useState<any[]>([]);
    const [activeTab, setActiveTab] = useState('overview');
    const [runs, setRuns] = useState<any[]>([]);

    const [formData, setFormData] = useState({ name: '', description: '', system_prompt: '', model: '', provider_id: '' });

    const fetchData = useCallback(async () => {
        try {
            const [agentRes, statsRes, provRes] = await Promise.all([
                axios.get(`/api/agents/${id}`),
                axios.get(`/api/agents/${id}/stats`),
                axios.get('/api/providers')
            ]);
            setAgent(agentRes.data);
            setStats(statsRes.data);
            setProviders(provRes.data || []);
            setFormData({
                name: agentRes.data.name,
                description: agentRes.data.description || '',
                system_prompt: agentRes.data.system_prompt,
                model: agentRes.data.model || '',
                provider_id: agentRes.data.provider_id?.toString() || ''
            });
        } catch (e) {
            console.error(e);
        }
        try {
            const runsRes = await axios.get(`/api/agents/${id}/runs`);
            setRuns(runsRes.data || []);
        } catch (e) {
            console.error(e);
        }
    }, [id]);

    useEffect(() => {
        fetchData();
    }, [fetchData]);

    const handleSave = async (e: React.FormEvent) => {
        e.preventDefault();
        try {
            const payload = {
                ...formData,
                provider_id: formData.provider_id ? parseInt(formData.provider_id) : null
            };
            await axios.put(`/api/agents/${id}`, payload);
            fetchData();
            alert('Saved successfully!');
        } catch (e) {
            console.error(e);
            alert('Failed to save');
        }
    };

    if (!agent) return <div>Loading...</div>;

    return (
        <div className="h-full flex flex-col">
            <div className="mb-6 flex items-center space-x-4">
                <Link to={`/companies/${shortName}/agents`} className="text-gray-500 hover:text-gray-900"><ArrowLeft size={20} /></Link>
                <h1 className="text-2xl font-bold">{agent.name}</h1>
            </div>

            <div className="border-b mb-6">
                <nav className="-mb-px flex space-x-8">
                    {['overview', 'logs', 'settings'].map(tab => (
                        <button
                            key={tab}
                            onClick={() => setActiveTab(tab)}
                            className={`${activeTab === tab ? 'border-indigo-500 text-indigo-600' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'} whitespace-nowrap py-4 px-1 border-b-2 font-medium text-sm capitalize`}
                        >
                            {tab}
                        </button>
                    ))}
                </nav>
            </div>

            <div className="flex-1 overflow-y-auto">
                {activeTab === 'overview' && (
                    <div className="space-y-6">
                        <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
                            <div className="bg-white p-4 rounded-lg shadow border">
                                <p className="text-sm text-gray-500 mb-1">Total Requests</p>
                                <p className="text-2xl font-bold">{stats?.total_requests || 0}</p>
                            </div>
                            <div className="bg-white p-4 rounded-lg shadow border">
                                <p className="text-sm text-gray-500 mb-1">Total Tokens</p>
                                <p className="text-2xl font-bold">{stats?.total_tokens || 0}</p>
                            </div>
                            <div className="bg-white p-4 rounded-lg shadow border">
                                <p className="text-sm text-gray-500 mb-1">Prompt Tokens</p>
                                <p className="text-2xl font-bold">{stats?.prompt_tokens || 0}</p>
                            </div>
                            <div className="bg-white p-4 rounded-lg shadow border">
                                <p className="text-sm text-gray-500 mb-1">Completion Tokens</p>
                                <p className="text-2xl font-bold">{stats?.completion_tokens || 0}</p>
                            </div>
                        </div>

                        <div className="bg-white p-6 rounded-lg shadow border">
                            <h3 className="font-bold mb-4">Configuration</h3>
                            <div className="space-y-4">
                                <div>
                                    <p className="text-sm text-gray-500 mb-1">Active Provider</p>
                                    <p className="font-medium">{providers.find(p => p.id === agent.provider_id)?.name || 'None'}</p>
                                </div>
                                <div>
                                    <p className="text-sm text-gray-500 mb-1">Active Model</p>
                                    <p className="font-medium">{agent.model || 'None'}</p>
                                </div>
                                <div className="pt-2">
                                    <p className="text-sm text-gray-500 mb-2">Proxy URL for CLI</p>
                                    <p className="font-mono text-sm bg-gray-100 p-2 rounded break-all">
                                        {window.location.protocol}//{window.location.host}/api/proxy/agent/{agent.id}/v1/chat/completions
                                    </p>
                                </div>
                            </div>
                        </div>
                    </div>
                )}

                {activeTab === 'logs' && (
                    <div className="space-y-4">
                        {runs.length === 0 ? (
                            <p className="text-sm text-gray-500 italic">No runs yet.</p>
                        ) : (
                            runs.map((r: any) => (
                                <details key={r.id} className="bg-gray-50 border rounded p-4 text-sm">
                                    <summary className="font-semibold cursor-pointer text-indigo-700">Run #{r.id} for Task #{r.task_id} ({r.status})</summary>
                                    <pre className="mt-2 text-xs bg-gray-900 text-green-400 p-2 rounded overflow-x-auto whitespace-pre-wrap">
                                        {r.log_content}
                                    </pre>
                                </details>
                            ))
                        )}
                    </div>
                )}

                {activeTab === 'settings' && (
                    <form onSubmit={handleSave} className="bg-white p-6 rounded-lg shadow border max-w-2xl space-y-4">
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Name</label>
                            <input type="text" required value={formData.name} onChange={e => setFormData({...formData, name: e.target.value})} className="w-full border rounded p-2" />
                        </div>
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
                            <input type="text" value={formData.description} onChange={e => setFormData({...formData, description: e.target.value})} className="w-full border rounded p-2" />
                        </div>
                        <div>
                            <div className="flex justify-between items-center mb-1">
                                <label className="block text-sm font-medium text-gray-700">LLM Provider</label>
                                <Link to={`/companies/${shortName}/providers`} className="text-xs text-indigo-600 hover:text-indigo-800">Manage Providers</Link>
                            </div>
                            <select value={formData.provider_id || ''} onChange={e => {
                                const selectedProviderId = e.target.value;
                                const provider = providers.find(p => p.id.toString() === selectedProviderId);
                                setFormData({...formData, provider_id: selectedProviderId, model: provider?.default_model || ''});
                            }} className="w-full border rounded p-2">
                                <option value="">-- Select Provider --</option>
                                {providers.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                            </select>
                        </div>
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Model Name</label>
                            <select required value={formData.model || ''} onChange={e => setFormData({...formData, model: e.target.value})} className="w-full border rounded p-2">
                                <option value="">-- Select Model --</option>
                                {providers.find(p => p.id.toString() === formData.provider_id)?.supported_models?.split(',').map((m: string) => m.trim()).filter((m: string) => m).map((m: string) => (
                                    <option key={m} value={m}>{m}</option>
                                ))}
                            </select>
                        </div>
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">System Prompt</label>
                            <textarea required rows={5} value={formData.system_prompt} onChange={e => setFormData({...formData, system_prompt: e.target.value})} className="w-full border rounded p-2 font-mono text-sm" />
                        </div>
                        <div className="pt-4">
                            <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700">
                                <Save size={16} className="mr-2" /> Save Changes
                            </button>
                        </div>
                    </form>
                )}
            </div>
        </div>
    );
};
