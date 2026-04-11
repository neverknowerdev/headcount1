import React, { useState, useEffect } from 'react';
import axios from 'axios';

export const Settings: React.FC = () => {
    const [basePath, setBasePath] = useState('');
    const [saving, setSaving] = useState(false);

    useEffect(() => {
        const fetchSettings = async () => {
            try {
                const res = await axios.get('/api/settings');
                if (res.data && res.data.base_path) {
                    setBasePath(res.data.base_path);
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
            await axios.post('/api/settings', { base_path: basePath });
            alert('Settings saved!');
        } catch (e) {
            console.error(e);
            alert('Failed to save settings');
        } finally {
            setSaving(false);
        }
    };

    return (
        <div className="max-w-2xl">
            <h1 className="text-2xl font-bold mb-6">Global Settings</h1>

            <div className="bg-white p-6 rounded-lg shadow-sm border">
                <form onSubmit={handleSave} className="space-y-4">
                    <div>
                        <label className="block text-sm font-medium text-gray-700 mb-1">
                            Workspace Root Directory
                        </label>
                        <p className="text-xs text-gray-500 mb-3">
                            This is where all company, project, and skill files will be stored physically on your disk. Changing this will not move existing files.
                        </p>
                        <input
                            type="text"
                            value={basePath}
                            onChange={e => setBasePath(e.target.value)}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border"
                            placeholder="/home/user/.paperclip2"
                        />
                    </div>

                    <button
                        type="submit"
                        disabled={saving}
                        className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700 disabled:bg-indigo-400"
                    >
                        {saving ? 'Saving...' : 'Save Settings'}
                    </button>
                </form>
            </div>
        </div>
    );
};
