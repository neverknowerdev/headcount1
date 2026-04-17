import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useStore } from '../store';
import { Plus, Calendar, ArrowLeft } from 'lucide-react';
import { Link } from 'react-router-dom';

export const SprintsManager: React.FC = () => {
    const { selectedCompanyId } = useStore();
    const [sprints, setSprints] = useState<any[]>([]);
    const [isModalOpen, setIsModalOpen] = useState(false);
    const [formData, setFormData] = useState({ name: '', goal: '', start_date: '', end_date: '' });

    const fetchSprints = async () => {
        if (!selectedCompanyId) return;
        try {
            const res = await axios.get(`/api/sprints?company_id=${selectedCompanyId}`);
            setSprints(res.data || []);
        } catch (e) {
            console.error(e);
        }
    };

    useEffect(() => {
        fetchSprints();
    }, [selectedCompanyId]);

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        try {
            const payload = {
                company_id: selectedCompanyId,
                name: formData.name,
                goal: formData.goal,
                start_date: formData.start_date ? new Date(formData.start_date).toISOString() : null,
                end_date: formData.end_date ? new Date(formData.end_date).toISOString() : null,
            };
            await axios.post('/api/sprints', payload);
            setIsModalOpen(false);
            setFormData({ name: '', goal: '', start_date: '', end_date: '' });
            fetchSprints();
        } catch (e) {
            console.error(e);
            alert("Failed to create sprint");
        }
    };

    return (
        <div className="h-full flex flex-col space-y-6">
            <div className="flex justify-between items-center">
                <div className="flex items-center space-x-4">
                    <Link to="/tasks" className="text-gray-500 hover:text-gray-900"><ArrowLeft size={20} /></Link>
                    <h1 className="text-2xl font-bold">Manage Sprints</h1>
                </div>
                <button
                    onClick={() => setIsModalOpen(true)}
                    className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700"
                >
                    <Plus size={16} className="mr-2" /> Create Sprint
                </button>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
                {sprints.map(s => (
                    <div key={s.id} className="bg-white p-6 rounded-lg border shadow-sm flex flex-col relative overflow-hidden">
                        <div className="flex items-center mb-4 text-indigo-600">
                            <Calendar className="mr-2" />
                            <h3 className="text-lg font-bold text-gray-900">{s.name}</h3>
                        </div>
                        {s.goal && <p className="text-sm text-gray-600 mb-4">{s.goal}</p>}
                        <div className="mt-auto space-y-1">
                            {s.start_date && <p className="text-xs text-gray-500"><span className="font-semibold">Start:</span> {new Date(s.start_date).toLocaleDateString()}</p>}
                            {s.end_date && <p className="text-xs text-gray-500"><span className="font-semibold">End:</span> {new Date(s.end_date).toLocaleDateString()}</p>}
                        </div>
                    </div>
                ))}
                {sprints.length === 0 && (
                    <div className="col-span-full text-center text-gray-500 py-10">No sprints created yet.</div>
                )}
            </div>

            {isModalOpen && (
                <div className="fixed inset-0 bg-black/50 flex justify-center items-center z-50">
                    <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-md">
                        <h2 className="text-xl font-bold mb-4">Create Sprint</h2>
                        <form onSubmit={handleSubmit} className="space-y-4">
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Sprint Name</label>
                                <input required type="text" value={formData.name} onChange={e => setFormData({...formData, name: e.target.value})} placeholder="e.g. Sprint 1" className="w-full border rounded p-2" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Goal / Description</label>
                                <input type="text" value={formData.goal} onChange={e => setFormData({...formData, goal: e.target.value})} className="w-full border rounded p-2" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Start Date</label>
                                <input type="date" required value={formData.start_date} onChange={e => setFormData({...formData, start_date: e.target.value})} className="w-full border rounded p-2" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">End Date</label>
                                <input type="date" required value={formData.end_date} onChange={e => setFormData({...formData, end_date: e.target.value})} className="w-full border rounded p-2" />
                            </div>
                            <div className="flex justify-end space-x-3 pt-4">
                                <button type="button" onClick={() => setIsModalOpen(false)} className="text-gray-500 hover:text-gray-700">Cancel</button>
                                <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700">Create</button>
                            </div>
                        </form>
                    </div>
                </div>
            )}
        </div>
    );
};
