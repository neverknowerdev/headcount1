import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { Plus, Trash2, Edit2, Play } from 'lucide-react';

export const ProvidersManager: React.FC = () => {
    const [providers, setProviders] = useState<any[]>([]);
    const [isModalOpen, setIsModalOpen] = useState(false);
    const [editingId, setEditingId] = useState<number | null>(null);
    const [formData, setFormData] = useState({ name: '', base_url: '', api_key: '', default_model: '', supported_models: '' });
    const [testResult, setTestResult] = useState<{status?: string, error?: string, log?: string} | null>(null);
    const [isSaving, setIsSaving] = useState(false);

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

    const testSingleModel = async (model: string, base_url: string, api_key: string) => {
        try {
            const res = await axios.post('/api/providers/test', {
                base_url,
                api_key,
                model
            });
            return res.data;
        } catch (e: any) {
            throw new Error(`Model ${model} failed: ${e.response?.data?.error || 'Unknown error'}`);
        }
    };

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        setIsSaving(true);
        setTestResult(null);

        try {
            const modelsToTest = [];
            if (formData.default_model) modelsToTest.push(formData.default_model.trim());
            if (formData.supported_models) {
                const supported = formData.supported_models.split(',').map(m => m.trim()).filter(m => m);
                modelsToTest.push(...supported);
            }

            // Deduplicate models
            const uniqueModels = Array.from(new Set(modelsToTest));

            // Test all configured models before saving
            for (const model of uniqueModels) {
                if (!editingId || formData.api_key) {
                     await testSingleModel(model, formData.base_url, formData.api_key);
                }
            }

            if (editingId) {
                await axios.put(`/api/providers/${editingId}`, formData);
            } else {
                await axios.post('/api/providers', formData);
            }

            setIsModalOpen(false);
            setEditingId(null);
            setFormData({ name: '', base_url: '', api_key: '', default_model: '', supported_models: '' });
            fetchProviders();
        } catch (e: any) {
            console.error(e);
            setTestResult({ error: e.message || "Save failed" });
        } finally {
            setIsSaving(false);
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
        try {
            // Test default model if available, else fallback
            const modelToTest = provider.default_model || 'gpt-3.5-turbo';
            const res = await testSingleModel(modelToTest, provider.base_url, provider.api_key);
            setTestResult(res);
        } catch (e: any) {
            setTestResult({ error: e.message || 'Unknown error' });
        }
    };

    return (
        <div className="h-full flex flex-col space-y-6">
            <div className="flex justify-between items-center">
                <h1 className="text-2xl font-bold">LLM Providers</h1>
                <button
                    onClick={() => {
                        setEditingId(null);
                        setFormData({ name: '', base_url: '', api_key: '', default_model: '', supported_models: '' });
                        setIsModalOpen(true);
                    }}
                    className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700"
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
                                <button onClick={() => {
                                    setEditingId(p.id);
                                    setFormData({ name: p.name, base_url: p.base_url, api_key: '', default_model: p.default_model || '', supported_models: p.supported_models || '' });
                                    setIsModalOpen(true);
                                }} className="text-gray-500 hover:text-gray-700">
                                    <Edit2 size={18} />
                                </button>
                                <button onClick={() => handleDelete(p.id)} className="text-red-500 hover:text-red-700">
                                    <Trash2 size={18} />
                                </button>
                            </div>
                        </div>
                        <p className="text-sm text-gray-600 mb-1 truncate"><span className="font-semibold">URL:</span> {p.base_url}</p>
                        {p.default_model && <p className="text-sm text-gray-600 mb-1"><span className="font-semibold">Default Model:</span> {p.default_model}</p>}
                        {p.supported_models && <p className="text-xs text-gray-500 mt-2 truncate"><span className="font-semibold">Models:</span> {p.supported_models}</p>}
                    </div>
                ))}
            </div>

            {testResult && (
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
                    <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-md">
                        <h2 className="text-xl font-bold mb-4">{editingId ? 'Edit Provider' : 'Add Provider'}</h2>
                        <form onSubmit={handleSubmit} className="space-y-4">
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
                                <input type="text" value={formData.default_model} onChange={e => setFormData({...formData, default_model: e.target.value})} placeholder="e.g. gpt-4o" className="w-full border rounded p-2" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Supported Models (comma-separated)</label>
                                <input type="text" value={formData.supported_models} onChange={e => setFormData({...formData, supported_models: e.target.value})} placeholder="e.g. gpt-4o, gpt-3.5-turbo" className="w-full border rounded p-2" />
                                <p className="text-xs text-gray-500 mt-1">All models entered will be tested on save.</p>
                            </div>
                            <div className="flex justify-end space-x-3 pt-4 border-t mt-6">
                                <button type="button" onClick={() => setIsModalOpen(false)} className="text-gray-500 hover:text-gray-700">Cancel</button>
                                <button type="submit" disabled={isSaving} className={`bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 flex items-center ${isSaving ? 'opacity-50 cursor-not-allowed' : ''}`}>
                                    {isSaving ? 'Testing & Saving...' : 'Save'}
                                </button>
                            </div>
                        </form>
                    </div>
                </div>
            )}
        </div>
    );
};
