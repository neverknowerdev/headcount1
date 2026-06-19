import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useNavigate } from 'react-router-dom';
import { useStore } from '../store';

interface BackupStatus {
    latest_backup: string | null;
    backups: string[];
}

interface RestoreResult {
    companies_restored: number;
    projects_restored: number;
    agents_restored: number;
    tasks_restored: number;
    runs_restored: number;
    providers_restored: number;
    sprints_restored: number;
    skills_restored: number;
    comments_restored: number;
    attachments_restored: number;
}

export const Backup: React.FC = () => {
    const navigate = useNavigate();
    const { selectedCompanyId, companies } = useStore();
    const [backupStatus, setBackupStatus] = useState<BackupStatus | null>(null);
    const [backingUp, setBackingUp] = useState(false);
    const [restoring, setRestoring] = useState(false);
    const [selectedArchive, setSelectedArchive] = useState('');
    const [restoreResult, setRestoreResult] = useState<RestoreResult | null>(null);
    const [loading, setLoading] = useState(true);

    const currentCompany = companies.find(c => c.id === selectedCompanyId);
    const shortName = currentCompany?.short_name || '';

    useEffect(() => {
        fetchBackupStatus();
    }, []);

    const fetchBackupStatus = async () => {
        try {
            const res = await axios.get('/api/backup/status');
            setBackupStatus(res.data);
        } catch (e) {
            console.error(e);
        } finally {
            setLoading(false);
        }
    };

    const handleBackup = async () => {
        setBackingUp(true);
        try {
            await axios.post('/api/backup');
            alert('Backup created successfully!');
            await fetchBackupStatus();
        } catch (e) {
            console.error(e);
            alert('Failed to create backup');
        } finally {
            setBackingUp(false);
        }
    };

    const handleRestore = async () => {
        if (!selectedArchive) {
            alert('Please select a backup to restore');
            return;
        }

        if (!confirm('This will wipe all current data and restore from the selected backup. Continue?')) {
            return;
        }

        setRestoring(true);
        setRestoreResult(null);
        try {
            const res = await axios.post('/api/backup/restore', {
                archive_path: selectedArchive,
            });
            setRestoreResult(res.data.result);
            alert('Backup restored successfully!');
        } catch (e) {
            console.error(e);
            alert('Failed to restore backup');
        } finally {
            setRestoring(false);
        }
    };

    if (loading) {
        return (
            <div className="max-w-2xl">
                <h1 className="text-2xl font-bold mb-6">Backup & Restore</h1>
                <div className="bg-white p-6 rounded-lg shadow-sm border">
                    <p className="text-gray-500">Loading...</p>
                </div>
            </div>
        );
    }

    return (
        <div className="max-w-2xl">
            <div className="flex items-center gap-3 mb-6">
                <button
                    onClick={() => navigate(`/companies/${shortName}/settings`)}
                    className="text-gray-500 hover:text-gray-700"
                >
                    ← Back to Settings
                </button>
                <h1 className="text-2xl font-bold">Backup & Restore</h1>
            </div>

            {/* Backup Section */}
            <div className="bg-white p-6 rounded-lg shadow-sm border mb-6">
                <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4">Create Backup</h2>
                <p className="text-sm text-gray-600 mb-4">
                    Create a backup of all your data including settings, companies, projects, tasks, agents, and memory files.
                    Backups are stored as compressed archives.
                </p>

                {backupStatus?.latest_backup && (
                    <div className="bg-gray-50 rounded-md p-3 mb-4">
                        <p className="text-sm text-gray-600">
                            <span className="font-medium">Latest backup:</span> {backupStatus.latest_backup}
                        </p>
                    </div>
                )}

                <button
                    onClick={handleBackup}
                    disabled={backingUp}
                    className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700 disabled:bg-indigo-400"
                >
                    {backingUp ? 'Creating Backup...' : 'Backup Now'}
                </button>
            </div>

            {/* Restore Section */}
            <div className="bg-white p-6 rounded-lg shadow-sm border mb-6">
                <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4">Restore from Backup</h2>
                <p className="text-sm text-gray-600 mb-4">
                    Select a backup to restore. This will wipe all current data and replace it with the backup contents.
                </p>

                {backupStatus?.backups && backupStatus.backups.length > 0 ? (
                    <div className="space-y-4">
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">
                                Select Backup
                            </label>
                            <select
                                value={selectedArchive}
                                onChange={e => setSelectedArchive(e.target.value)}
                                className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border"
                            >
                                <option value="">Choose a backup...</option>
                                {backupStatus.backups.map(backup => (
                                    <option key={backup} value={backup}>
                                        {backup}
                                    </option>
                                ))}
                            </select>
                        </div>

                        <button
                            onClick={handleRestore}
                            disabled={restoring || !selectedArchive}
                            className="bg-orange-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-orange-700 disabled:bg-orange-400"
                        >
                            {restoring ? 'Restoring...' : 'Restore Backup'}
                        </button>
                    </div>
                ) : (
                    <p className="text-sm text-gray-500">No backups available.</p>
                )}
            </div>

            {/* Restore Result */}
            {restoreResult && (
                <div className="bg-green-50 border border-green-200 p-6 rounded-lg shadow-sm">
                    <h2 className="text-lg font-medium text-green-800 border-b border-green-200 pb-2 mb-4">
                        Restore Complete
                    </h2>
                    <div className="grid grid-cols-2 gap-3 text-sm">
                        <div><span className="font-medium">Companies:</span> {restoreResult.companies_restored}</div>
                        <div><span className="font-medium">Projects:</span> {restoreResult.projects_restored}</div>
                        <div><span className="font-medium">Agents:</span> {restoreResult.agents_restored}</div>
                        <div><span className="font-medium">Tasks:</span> {restoreResult.tasks_restored}</div>
                        <div><span className="font-medium">Runs:</span> {restoreResult.runs_restored}</div>
                        <div><span className="font-medium">Providers:</span> {restoreResult.providers_restored}</div>
                        <div><span className="font-medium">Sprints:</span> {restoreResult.sprints_restored}</div>
                        <div><span className="font-medium">Skills:</span> {restoreResult.skills_restored}</div>
                        <div><span className="font-medium">Comments:</span> {restoreResult.comments_restored}</div>
                        <div><span className="font-medium">Attachments:</span> {restoreResult.attachments_restored}</div>
                    </div>
                </div>
            )}

            {/* Backup List */}
            {backupStatus?.backups && backupStatus.backups.length > 0 && (
                <div className="bg-white p-6 rounded-lg shadow-sm border mt-6">
                    <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4">Available Backups</h2>
                    <ul className="space-y-2">
                        {backupStatus.backups.map(backup => (
                            <li key={backup} className="text-sm text-gray-600 flex items-center gap-2">
                                <span className="w-2 h-2 bg-green-400 rounded-full"></span>
                                {backup}
                            </li>
                        ))}
                    </ul>
                </div>
            )}
        </div>
    );
};
