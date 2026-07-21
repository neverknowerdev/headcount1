import { SecretLabel } from '../components/SecretField';
import React, { useState, useEffect, useRef } from 'react';
import axios from 'axios';
import { useStore, useIsOwner } from '../store';
import { useNavigate } from 'react-router-dom';

export const Settings: React.FC = () => {
    const navigate = useNavigate();
    const { selectedCompanyId, companies, setCompanies } = useStore();
    const isOwner = useIsOwner();

    const [companyShortName, setCompanyShortName] = useState('');
    const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);
    const [deleteConfirmText, setDeleteConfirmText] = useState('');
    const [deleting, setDeleting] = useState(false);

    useEffect(() => {
        const comp = companies.find(c => c.id === selectedCompanyId);
        if (comp) {
            setCompanyShortName(comp.short_name);
        }
    }, [selectedCompanyId, companies]);

    const [basePath, setBasePath] = useState('');
    const [saving, setSaving] = useState(false);
    const [sshKey, setSshKey] = useState('');
    const [sshFileName, setSshFileName] = useState('');
    const sshFileInputRef = useRef<HTMLInputElement>(null);

    useEffect(() => {
        const fetchSettings = async () => {
            try {
                const res = await axios.get('/api/settings');
                if (res.data) {
                    setBasePath(res.data.base_path || '');
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
            // The SSH key is per-user, encrypted at rest under your passkey.
            if (sshKey) {
                await axios.post('/api/settings/ssh', { key: sshKey });
                setSshKey('');
            }

            // The workspace root is instance-global (operator-managed). Saving it
            // is only possible when the operator has enabled the global admin API;
            // a 404 there is expected for regular users, so don't fail the save.
            try {
                await axios.post('/api/settings', { base_path: basePath });
            } catch (err: any) {
                if (err?.response?.status !== 404) throw err;
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

    const handleSshFileUpload = (e: React.ChangeEvent<HTMLInputElement>) => {
        const file = e.target.files?.[0];
        if (!file) return;
        setSshFileName(file.name);
        const reader = new FileReader();
        reader.onload = ev => setSshKey((ev.target?.result as string) || '');
        reader.readAsText(file);
    };

    const handleDeleteCompany = async () => {
        const currentCompany = companies.find(c => c.id === selectedCompanyId);
        if (!currentCompany || deleteConfirmText !== currentCompany.short_name) {
            return;
        }

        setDeleting(true);
        try {
            await axios.delete(`/api/companies/${currentCompany.id}`);
            const updatedCompanies = companies.filter(c => c.id !== selectedCompanyId);
            setCompanies(updatedCompanies);

            if (updatedCompanies.length > 0) {
                navigate(`/companies/${updatedCompanies[0].short_name}`, { replace: true });
            } else {
                navigate('/add-company', { replace: true });
            }
        } catch (e) {
            console.error(e);
            alert('Failed to delete company');
        } finally {
            setDeleting(false);
            setShowDeleteConfirm(false);
            setDeleteConfirmText('');
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
                            placeholder="/home/user/.headcount1"
                        />
                    </div>
                    <div className="bg-indigo-50 border border-indigo-100 rounded-md p-3 text-sm text-indigo-900">
                        Models used for lightweight internal calls (commit messages, artifact Q&A) are configured under <strong>Default Models</strong> on the{' '}
                        <a href={`/companies/${companyShortName}/providers`} className="underline hover:text-indigo-700">LLM Providers</a> page.
                    </div>
                    <div>
                        <SecretLabel>SSH Private Key</SecretLabel>
                        <p className="text-xs text-gray-500 mb-2">
                            Your personal key to authenticate Git operations for private repositories.
                            Encrypted at rest under your passkey; never shared with other users. Paste the key or upload the file directly.
                        </p>
                        <textarea
                            value={sshKey}
                            onChange={e => { setSshKey(e.target.value); setSshFileName(''); }}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border font-mono"
                            rows={4}
                            placeholder="-----BEGIN OPENSSH PRIVATE KEY-----..."
                        />
                        <div className="mt-2 flex items-center gap-3">
                            <button
                                type="button"
                                onClick={() => sshFileInputRef.current?.click()}
                                className="text-sm text-indigo-600 hover:text-indigo-800 border border-indigo-300 rounded px-3 py-1"
                            >
                                Upload from file
                            </button>
                            {sshFileName && (
                                <span className="text-xs text-gray-500 font-mono">{sshFileName}</span>
                            )}
                            <input
                                ref={sshFileInputRef}
                                type="file"
                                accept=".pem,.key,*"
                                className="hidden"
                                onChange={handleSshFileUpload}
                            />
                        </div>
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
                            onClick={() => navigate(`/companies/${companyShortName}/backup`)}
                            className="bg-blue-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-blue-700"
                        >
                            Export & Import
                        </button>
                    </div>
                </form>
            </div>

            {isOwner && <div className="bg-white p-6 rounded-lg shadow-sm border border-red-200 mt-8">
                <h2 className="text-lg font-medium text-red-600 border-b border-red-200 pb-2 mb-4">Danger Zone</h2>
                <div className="flex items-center justify-between">
                    <div>
                        <h3 className="text-sm font-medium text-gray-900">Delete this company</h3>
                        <p className="text-xs text-gray-500 mt-1">
                            Once deleted, it will be gone forever. All data will be archived before deletion.
                        </p>
                    </div>
                    <button
                        type="button"
                        onClick={() => setShowDeleteConfirm(true)}
                        className="bg-red-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-red-700"
                    >
                        Delete Company
                    </button>
                </div>
            </div>}

            {showDeleteConfirm && (
                <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
                    <div className="bg-white rounded-lg p-6 max-w-md w-full mx-4">
                        <h3 className="text-lg font-bold text-red-600 mb-4">Delete Company</h3>
                        <div className="bg-red-50 border border-red-200 rounded-md p-4 mb-4">
                            <p className="text-sm text-red-800 font-medium mb-2">
                                Warning: This action cannot be undone!
                            </p>
                            <p className="text-sm text-red-700">
                                This will permanently delete the company and all associated data including:
                            </p>
                            <ul className="text-sm text-red-700 list-disc list-inside mt-2">
                                <li>All projects and tasks</li>
                                <li>All agents and their configurations</li>
                                <li>All comments and run logs</li>
                                <li>All workspace files</li>
                            </ul>
                            <p className="text-sm text-red-700 mt-2">
                                Your files will be archived before deletion, but the company data cannot be restored.
                            </p>
                        </div>
                        <p className="text-sm text-gray-700 mb-2">
                            To confirm, type the company short name: <span className="font-mono font-bold">{companyShortName}</span>
                        </p>
                        <input
                            type="text"
                            value={deleteConfirmText}
                            onChange={e => setDeleteConfirmText(e.target.value)}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-red-500 focus:border-red-500 sm:text-sm p-2 border mb-4"
                            placeholder={companyShortName}
                        />
                        <div className="flex gap-3 justify-end">
                            <button
                                type="button"
                                onClick={() => {
                                    setShowDeleteConfirm(false);
                                    setDeleteConfirmText('');
                                }}
                                className="px-4 py-2 border border-gray-300 rounded-md text-gray-700 hover:bg-gray-50"
                            >
                                Cancel
                            </button>
                            <button
                                type="button"
                                onClick={handleDeleteCompany}
                                disabled={deleteConfirmText !== companyShortName || deleting}
                                className="bg-red-600 text-white px-4 py-2 rounded-md hover:bg-red-700 disabled:bg-red-300 disabled:cursor-not-allowed"
                            >
                                {deleting ? 'Deleting...' : 'Delete Company'}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};
