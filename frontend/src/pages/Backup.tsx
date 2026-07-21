import React, { useState } from 'react';
import axios from 'axios';
import { useNavigate } from 'react-router-dom';
import { useStore } from '../store';

interface ImportResult {
    companies_restored: number;
    projects_restored: number;
    agents_restored: number;
    tasks_restored: number;
    runs_restored: number;
    providers_restored: number;
    sprints_restored: number;
    skills_restored: number;
    artifacts_restored: number;
}

export const Backup: React.FC = () => {
    const navigate = useNavigate();
    const { selectedCompanyId, companies } = useStore();
    const [exporting, setExporting] = useState(false);
    const [importing, setImporting] = useState(false);
    const [importFile, setImportFile] = useState<File | null>(null);
    const [importResult, setImportResult] = useState<ImportResult | null>(null);

    const currentCompany = companies.find(c => c.id === selectedCompanyId);
    const shortName = currentCompany?.short_name || '';

    const handleExport = async () => {
        setExporting(true);
        try {
            const res = await axios.get('/api/data/export', { responseType: 'blob' });
            // Derive the download filename from the Content-Disposition header,
            // falling back to a timestamped default.
            const disposition = res.headers['content-disposition'] || '';
            const match = /filename="?([^"]+)"?/.exec(disposition);
            const filename = match ? match[1] : `headcount1-export-${Date.now()}.tar.gz`;

            const url = window.URL.createObjectURL(new Blob([res.data]));
            const a = document.createElement('a');
            a.href = url;
            a.download = filename;
            document.body.appendChild(a);
            a.click();
            a.remove();
            window.URL.revokeObjectURL(url);
        } catch (e) {
            console.error(e);
            alert('Failed to export your data');
        } finally {
            setExporting(false);
        }
    };

    const handleImport = async () => {
        if (!importFile) {
            alert('Please choose an export archive to import');
            return;
        }
        if (!confirm('This will import the data from the selected archive into your account, creating new copies of every company, project and task it contains. Continue?')) {
            return;
        }

        setImporting(true);
        setImportResult(null);
        try {
            const form = new FormData();
            form.append('file', importFile);
            const res = await axios.post('/api/data/import', form, {
                headers: { 'Content-Type': 'multipart/form-data' },
            });
            setImportResult(res.data.result);
            alert('Data imported successfully!');
        } catch (e) {
            console.error(e);
            alert('Failed to import data');
        } finally {
            setImporting(false);
        }
    };

    return (
        <div className="max-w-2xl">
            <div className="flex items-center gap-3 mb-6">
                <button
                    onClick={() => navigate(`/companies/${shortName}/settings`)}
                    className="text-gray-500 hover:text-gray-700"
                >
                    ← Back to Settings
                </button>
                <h1 className="text-2xl font-bold">Export & Import</h1>
            </div>

            {/* Export Section */}
            <div className="bg-white p-6 rounded-lg shadow-sm border mb-6">
                <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4">Export my data</h2>
                <p className="text-sm text-gray-600 mb-4">
                    Download an archive of <span className="font-medium">your own</span> companies, projects,
                    tasks, agents, providers and their files. Your encrypted secrets are included as
                    ciphertext and stay decryptable with your passkey. The archive contains only your data —
                    no other users are included.
                </p>
                <button
                    onClick={handleExport}
                    disabled={exporting}
                    className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700 disabled:bg-indigo-400"
                >
                    {exporting ? 'Exporting...' : 'Export my data'}
                </button>
            </div>

            {/* Import Section */}
            <div className="bg-white p-6 rounded-lg shadow-sm border mb-6">
                <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4">Import data</h2>
                <p className="text-sm text-gray-600 mb-4">
                    Import an export archive into your account. New companies, projects and tasks are created
                    with fresh IDs and bound to you — nothing you (or anyone else) already has is overwritten.
                </p>

                <div className="space-y-4">
                    <input
                        type="file"
                        accept=".tar.gz,.gz,application/gzip"
                        onChange={e => setImportFile(e.target.files?.[0] || null)}
                        className="block w-full text-sm text-gray-600 file:mr-4 file:py-2 file:px-4 file:rounded-md file:border-0 file:text-sm file:font-medium file:bg-indigo-50 file:text-indigo-700 hover:file:bg-indigo-100"
                    />
                    <button
                        onClick={handleImport}
                        disabled={importing || !importFile}
                        className="bg-orange-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-orange-700 disabled:bg-orange-400"
                    >
                        {importing ? 'Importing...' : 'Import data'}
                    </button>
                </div>
            </div>

            {/* Import Result */}
            {importResult && (
                <div className="bg-green-50 border border-green-200 p-6 rounded-lg shadow-sm">
                    <h2 className="text-lg font-medium text-green-800 border-b border-green-200 pb-2 mb-4">
                        Import Complete
                    </h2>
                    <div className="grid grid-cols-2 gap-3 text-sm">
                        <div><span className="font-medium">Companies:</span> {importResult.companies_restored}</div>
                        <div><span className="font-medium">Projects:</span> {importResult.projects_restored}</div>
                        <div><span className="font-medium">Agents:</span> {importResult.agents_restored}</div>
                        <div><span className="font-medium">Tasks:</span> {importResult.tasks_restored}</div>
                        <div><span className="font-medium">Runs:</span> {importResult.runs_restored}</div>
                        <div><span className="font-medium">Providers:</span> {importResult.providers_restored}</div>
                        <div><span className="font-medium">Sprints:</span> {importResult.sprints_restored}</div>
                        <div><span className="font-medium">Skills:</span> {importResult.skills_restored}</div>
                        <div><span className="font-medium">Artifacts:</span> {importResult.artifacts_restored}</div>
                    </div>
                </div>
            )}
        </div>
    );
};
