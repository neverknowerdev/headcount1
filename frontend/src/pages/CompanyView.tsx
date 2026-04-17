/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useStore } from '../store';
import { Folder } from 'lucide-react';

export const CompanyView: React.FC = () => {
  const { selectedCompanyId } = useStore();
  const [projects, setProjects] = useState<any[]>([]);
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [formData, setFormData] = useState({ name: '', workspace_folder: '' });
  const [companyShortName, setCompanyShortName] = useState('');

  useEffect(() => {
    if (selectedCompanyId) {
        axios.get('/api/companies').then(res => {
            const comp = res.data.find((c: any) => c.id === selectedCompanyId);
            if (comp) setCompanyShortName(comp.short_name);
        });
    }
  }, [selectedCompanyId]);

  const fetchProjects = useCallback(async () => {
    if (!selectedCompanyId) return;
    try {
      const res = await axios.get(`/api/projects?company_id=${selectedCompanyId}`);
      setProjects(res.data || []);
    } catch (e) {
      console.error(e);
    }
  }, [selectedCompanyId]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    fetchProjects();
  }, [fetchProjects]);

  const createProject = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!formData.name || !selectedCompanyId) return;
    try {
      await axios.post('/api/projects', {
        company_id: selectedCompanyId,
        name: formData.name,
        workspace_folder: formData.workspace_folder
      });
      setFormData({ name: '', workspace_folder: '' });
      setIsModalOpen(false);
      fetchProjects();
    } catch (e) {
      console.error(e);
    }
  };

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Projects</h1>

      <div className="mb-8">
        <button onClick={() => setIsModalOpen(true)} className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700">
          Create Project
        </button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
        {projects.map(p => (
          <div key={p.id} className="block p-6 bg-white rounded-lg border shadow-sm hover:shadow-md transition">
            <div className="flex items-center mb-4 text-indigo-600">
                <Folder className="mr-2" />
                <h3 className="text-lg font-semibold text-gray-900">{p.name}</h3>
            </div>
            {p.description && <p className="text-sm text-gray-600 mt-2">{p.description}</p>}
            <p className="text-xs text-gray-400 mt-4">ID: {p.id}</p>
          </div>
        ))}
      </div>

      {isModalOpen && (
        <div className="fixed inset-0 bg-black/50 flex justify-center items-center z-50">
          <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-md">
            <h2 className="text-xl font-bold mb-4">Create Project</h2>
            <form onSubmit={createProject} className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Project Name</label>
                <input required type="text" value={formData.name} onChange={e => {
                    const name = e.target.value;
                    const folder = companyShortName ? `${companyShortName}/${name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}` : '';
                    setFormData({...formData, name, workspace_folder: folder});
                }} className="w-full border rounded p-2" placeholder="e.g. NextGen Mobile App" />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Workspace Folder (Relative to .paperclip2/)</label>
                <input type="text" value={formData.workspace_folder} onChange={e => setFormData({...formData, workspace_folder: e.target.value})} className="w-full border rounded p-2 text-sm" />
                <p className="text-xs text-gray-500 mt-1">This directory will be created automatically.</p>
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
