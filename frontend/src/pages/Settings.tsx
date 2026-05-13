import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useStore } from '../store';
import { useNavigate } from 'react-router-dom';

export const Settings: React.FC = () => {
    const navigate = useNavigate();
    const { selectedCompanyId, companies, setCompanies } = useStore();

    const [companyShortName, setCompanyShortName] = useState('');

    useEffect(() => {
        const comp = companies.find(c => c.id === selectedCompanyId);
        if (comp) {
            setCompanyShortName(comp.short_name);
        }
    }, [selectedCompanyId, companies]);

    const [basePath, setBasePath] = useState('');
    const [gitRemoteUrl, setGitRemoteUrl] = useState('');
    const [githubPat, setGithubPat] = useState('');
    const [systemLlmModel, setSystemLlmModel] = useState('');
    const [saving, setSaving] = useState(false);
    const [syncing, setSyncing] = useState(false);
    const [sshKey, setSshKey] = useState('');

    useEffect(() => {
        const fetchSettings = async () => {
            try {
                const res = await axios.get('/api/settings');
                if (res.data) {
                    setBasePath(res.data.base_path || '');
                    setGitRemoteUrl(res.data.git_remote_url || '');
                    setGithubPat(res.data.github_pat || '');
                    setSystemLlmModel(res.data.system_llm_model || '');
                }
            } catch (e) {
                console.error(e);
            }
        };
        fetchSettings();
    }, []);

    const handleSave = async (e: React.FormEvent) => {
        e.preventDefault();
        setSaving(true);
        try {
            await axios.post('/api/settings', {
                base_path: basePath,
                git_remote_url: gitRemoteUrl,
                github_pat: githubPat,
                system_llm_model: systemLlmModel
            });

            if (sshKey) {
                await axios.post('/api/settings/ssh', { key: sshKey });
                setSshKey('');
                alert('SSH Key uploaded successfully');
            }

            const currentCompany = companies.find(c => c.id === selectedCompanyId);
            if (currentCompany && companyShortName !== currentCompany.short_name) {
                await axios.put(`/api/companies/${currentCompany.id}`, { short_name: companyShortName });
                const updatedCompanies = companies.map(c =>
                    c.id === selectedCompanyId ? { ...c, short_name: companyShortName } : c
                );
                setCompanies(updatedCompanies);
                navigate(`/companies/${companyShortName}/settings`, { replace: true });
            }

            alert('Settings saved!');
        } catch (e) {
            console.error(e);
            alert('Failed to save settings');
        } finally {
            setSaving(false);
        }
    };

    const handleSync = async () => {
        setSyncing(true);
        try {
            await axios.post('/api/settings/sync');
            alert('Sync completed successfully!');
        } catch (e) {
            console.error(e);
            alert('Failed to sync settings from filesystem');
        } finally {
            setSyncing(false);
        }
    };

    return (
        <div className="max-w-2xl">
            <h1 className="text-2xl font-bold mb-6">Settings</h1>

            <div className="bg-white p-6 rounded-lg shadow-sm border">
                <form onSubmit={handleSave} className="space-y-4">
                    <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4">Company Settings</h2>
                    <div>
                        <label className="block text-sm font-medium text-gray-700 mb-1">
                            Company Short Name
                        </label>
                        <p className="text-xs text-gray-500 mb-3">
                            Used as a prefix for Agent CLI runs. Max 2 characters.
                        </p>
                        <input
                            type="text"
                            maxLength={2}
                            value={companyShortName}
                            onChange={e => setCompanyShortName(e.target.value.toLowerCase())}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border uppercase font-mono"
                            placeholder="ac"
                        />
                    </div>

                    <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4 mt-8">Global Settings</h2>
                    <div>
                        <label className="block text-sm font-medium text-gray-700 mb-1">
                            Workspace Root Directory
                        </label>
                        <input
                            type="text"
                            value={basePath}
                            onChange={e => setBasePath(e.target.value)}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border"
                            placeholder="/home/user/.paperclip2"
                        />
                    </div>
                    <div>
                        <label className="block text-sm font-medium text-gray-700 mb-1">
                            Data Git Remote URL
                        </label>
                        <input
                            type="text"
                            value={gitRemoteUrl}
                            onChange={e => setGitRemoteUrl(e.target.value)}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border"
                            placeholder="git@github.com:user/paperclip2-data.git"
                        />
                    </div>
                    <div>
                        <label className="block text-sm font-medium text-gray-700 mb-1">
                            GitHub PAT (Personal Access Token)
                        </label>
                        <input
                            type="password"
                            value={githubPat}
                            onChange={e => setGithubPat(e.target.value)}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border"
                            placeholder="ghp_..."
                        />
                    </div>
                    <div>
                        <label className="block text-sm font-medium text-gray-700 mb-1">
                            System LLM Model
                        </label>
                        <input
                            type="text"
                            value={systemLlmModel}
                            onChange={e => setSystemLlmModel(e.target.value)}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border"
                            placeholder="gpt-4o-mini"
                        />
                    </div>
                    <div>
                        <label className="block text-sm font-medium text-gray-700 mb-1">
                            Upload SSH Key
                        </label>
                        <p className="text-xs text-gray-500 mb-3">
                            Paste your private SSH key here to authenticate Git operations. It will be saved securely.
                        </p>
                        <textarea
                            value={sshKey}
                            onChange={e => setSshKey(e.target.value)}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border font-mono"
                            rows={4}
                            placeholder="-----BEGIN OPENSSH PRIVATE KEY-----..."
                        />
                    </div>

                    <div className="flex gap-4">
                        <button
                            type="submit"
                            disabled={saving}
                            className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700 disabled:bg-indigo-400"
                        >
                            {saving ? 'Saving...' : 'Save Settings'}
                        </button>

                        <button
                            type="button"
                            onClick={handleSync}
                            disabled={syncing}
                            className="bg-green-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-green-700 disabled:bg-green-400"
                        >
                            {syncing ? 'Syncing...' : 'Sync from Filesystem'}
                        </button>
                    </div>
                </form>
            </div>
        </div>
    );
};
