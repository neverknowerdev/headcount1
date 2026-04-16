/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useStore } from '../store';
import { Folder } from 'lucide-react';

export const CompanyView: React.FC = () => {
  const { selectedCompanyId } = useStore();
  const [projects, setProjects] = useState<any[]>([]);
  const [newProjectName, setNewProjectName] = useState('');

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
    if (!newProjectName || !selectedCompanyId) return;
    try {
      await axios.post('/api/projects', {
        company_id: selectedCompanyId,
        name: newProjectName
      });
      setNewProjectName('');
      fetchProjects();
    } catch (e) {
      console.error(e);
    }
  };

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Projects</h1>

      <form onSubmit={createProject} className="mb-8 flex gap-4">
        <input
          type="text"
          value={newProjectName}
          onChange={(e) => setNewProjectName(e.target.value)}
          placeholder="New Project Name"
          className="border border-gray-300 p-2 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500"
        />
        <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700">
          Create Project
        </button>
      </form>

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
    </div>
  );
};
