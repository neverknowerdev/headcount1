/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useStore } from '../store';
import { FileCode, Save } from 'lucide-react';

export const SkillsManager: React.FC = () => {
    const { selectedCompanyId } = useStore();
    const [skills, setSkills] = useState<any[]>([]);
    const [selectedSkillId, setSelectedSkillId] = useState<number | null>(null);
    const [files, setFiles] = useState<string[]>([]);
    const [selectedFile, setSelectedFile] = useState<string | null>(null);
    const [fileContent, setFileContent] = useState<string>('');
    const [newSkillName, setNewSkillName] = useState('');
    const [isSaving, setIsSaving] = useState(false);

    const fetchSkills = useCallback(async () => {
        if (!selectedCompanyId) return;
        try {
            const res = await axios.get(`/api/skills?company_id=${selectedCompanyId}`);
            setSkills(res.data || []);
        } catch (e) {
            console.error(e);
        }
    }, [selectedCompanyId]);

    const fetchFiles = useCallback(async () => {
        if (!selectedSkillId) return;
        try {
            const res = await axios.get(`/api/skills/${selectedSkillId}/files`);
            setFiles(res.data || []);
            setSelectedFile(null);
            setFileContent('');
        } catch (e) {
            console.error(e);
            setFiles([]);
        }
    }, [selectedSkillId]);

    const fetchFileContent = useCallback(async () => {
        if (!selectedSkillId || !selectedFile) return;
        try {
            const res = await axios.get(`/api/skills/${selectedSkillId}/files/content?path=${encodeURIComponent(selectedFile)}`);
            setFileContent(res.data || '');
        } catch (e) {
            console.error(e);
        }
    }, [selectedSkillId, selectedFile]);

    useEffect(() => { fetchSkills(); }, [fetchSkills]);
    useEffect(() => { fetchFiles(); }, [fetchFiles]);
    useEffect(() => { fetchFileContent(); }, [fetchFileContent]);

    const handleCreateSkill = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!newSkillName || !selectedCompanyId) return;
        try {
            const res = await axios.post('/api/skills', {
                company_id: selectedCompanyId,
                name: newSkillName,
                description: 'Custom skill'
            });
            setNewSkillName('');
            fetchSkills();
            setSelectedSkillId(res.data.id);
        } catch (e) {
            console.error(e);
        }
    };

    const handleSaveFile = async () => {
        if (!selectedSkillId || !selectedFile) return;
        setIsSaving(true);
        try {
            await axios.put(`/api/skills/${selectedSkillId}/files/content`, {
                path: selectedFile,
                content: fileContent
            });
            alert('Saved successfully!');
        } catch (e) {
            console.error(e);
            alert('Failed to save file');
        } finally {
            setIsSaving(false);
        }
    };

    const handleCreateFile = async () => {
        const filename = prompt('Enter new file name (e.g. main.py):');
        if (!filename || !selectedSkillId) return;

        try {
            await axios.put(`/api/skills/${selectedSkillId}/files/content`, {
                path: filename,
                content: '# New skill file'
            });
            fetchFiles();
            setSelectedFile(filename);
        } catch (e) {
            console.error(e);
            alert('Failed to create file');
        }
    };

    return (
        <div className="h-full flex flex-col space-y-6">
            <div className="flex justify-between items-center">
                <h1 className="text-2xl font-bold">Skills Manager</h1>
                <form onSubmit={handleCreateSkill} className="flex gap-2">
                    <input
                        type="text"
                        value={newSkillName}
                        onChange={(e) => setNewSkillName(e.target.value)}
                        placeholder="New Skill Name"
                        className="border border-gray-300 p-2 rounded-md text-sm shadow-sm"
                    />
                    <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded-md text-sm shadow-sm">
                        Create Skill
                    </button>
                </form>
            </div>

            <div className="flex flex-1 gap-6 min-h-0">
                {/* Skills List */}
                <div className="w-64 bg-white border rounded-lg shadow-sm flex flex-col">
                    <div className="p-4 border-b bg-gray-50 font-medium">Skills</div>
                    <div className="flex-1 overflow-y-auto p-2 space-y-1">
                        {skills.map(s => (
                            <button
                                key={s.id}
                                onClick={() => setSelectedSkillId(s.id)}
                                className={`w-full text-left px-3 py-2 rounded-md text-sm ${selectedSkillId === s.id ? 'bg-indigo-100 text-indigo-700 font-medium' : 'hover:bg-gray-100'}`}
                            >
                                {s.name}
                            </button>
                        ))}
                    </div>
                </div>

                {/* File Tree */}
                <div className="w-64 bg-white border rounded-lg shadow-sm flex flex-col">
                    <div className="p-4 border-b bg-gray-50 flex justify-between items-center">
                        <span className="font-medium">Files</span>
                        {selectedSkillId && (
                            <button onClick={handleCreateFile} className="text-indigo-600 text-sm hover:underline">+ New File</button>
                        )}
                    </div>
                    <div className="flex-1 overflow-y-auto p-2 space-y-1">
                        {!selectedSkillId ? (
                            <p className="text-gray-500 text-sm text-center mt-4">Select a skill</p>
                        ) : files.length === 0 ? (
                            <p className="text-gray-500 text-sm text-center mt-4 italic">No files found.</p>
                        ) : (
                            files.map(f => (
                                <button
                                    key={f}
                                    onClick={() => setSelectedFile(f)}
                                    className={`w-full text-left px-3 py-2 rounded-md text-sm flex items-center ${selectedFile === f ? 'bg-indigo-100 text-indigo-700 font-medium' : 'hover:bg-gray-100'}`}
                                >
                                    <FileCode size={14} className="mr-2" />
                                    {f}
                                </button>
                            ))
                        )}
                    </div>
                </div>

                {/* Editor */}
                <div className="flex-1 bg-white border rounded-lg shadow-sm flex flex-col">
                    <div className="p-4 border-b bg-gray-50 flex justify-between items-center">
                        <span className="font-medium">{selectedFile || 'Editor'}</span>
                        {selectedFile && (
                            <button
                                onClick={handleSaveFile}
                                disabled={isSaving}
                                className="flex items-center text-sm bg-indigo-600 text-white px-3 py-1.5 rounded disabled:bg-indigo-300"
                            >
                                <Save size={16} className="mr-1" /> Save
                            </button>
                        )}
                    </div>
                    <div className="flex-1 p-0">
                        {selectedFile ? (
                            <textarea
                                value={fileContent}
                                onChange={(e) => setFileContent(e.target.value)}
                                className="w-full h-full p-4 font-mono text-sm resize-none focus:outline-none focus:ring-0"
                                spellCheck={false}
                            />
                        ) : (
                            <div className="h-full flex items-center justify-center text-gray-400">
                                Select a file to edit
                            </div>
                        )}
                    </div>
                </div>
            </div>
        </div>
    );
};
