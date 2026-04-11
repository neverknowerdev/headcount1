import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';

export const CompanyView: React.FC = () => {
  const { companyId } = useParams();
  const [projects, setProjects] = useState<any[]>([]);
  const [newProjectName, setNewProjectName] = useState('');

  useEffect(() => {
    fetchProjects();
  }, [companyId]);

  const fetchProjects = async () => {
    try {
      const res = await axios.get(`/api/projects?company_id=${companyId}`);
      setProjects(res.data || []);
    } catch (e) {
      console.error(e);
    }
  };

  const createProject = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newProjectName) return;
    try {
      await axios.post('/api/projects', {
        company_id: parseInt(companyId || '0'),
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
      <h1 className="text-2xl font-bold mb-6">Company Projects</h1>

      <form onSubmit={createProject} className="mb-8 flex gap-4">
        <input
          type="text"
          value={newProjectName}
          onChange={(e) => setNewProjectName(e.target.value)}
          placeholder="New Project Name"
          className="border p-2 rounded"
        />
        <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded">
          Create Project
        </button>
      </form>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
        {projects.map(p => (
          <Link key={p.id} to={`/project/${p.id}`} className="block p-6 bg-white rounded-lg border shadow-sm hover:shadow-md transition">
            <h3 className="text-lg font-semibold">{p.name}</h3>
            {p.description && <p className="text-sm text-gray-600 mt-2">{p.description}</p>}
          </Link>
        ))}
      </div>
    </div>
  );
};
