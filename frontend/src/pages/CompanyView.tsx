/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { Link } from 'react-router-dom';
import { useStore, useIsOwner } from '../store';
import { Folder, GitBranch, ExternalLink, Search } from 'lucide-react';

export const CompanyView: React.FC = () => {
  const { selectedCompanyId } = useStore();
  const isOwner = useIsOwner();
  const [projects, setProjects] = useState<any[]>([]);
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [formData, setFormData] = useState({ name: '', workspace_folder: '', repository_url: '' });
  const [companyShortName, setCompanyShortName] = useState('');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
	const [githubRepos, setGithubRepos] = useState<any[]>([]);
	const [githubStatus, setGithubStatus] = useState<any>(null);
	const [repoSearch, setRepoSearch] = useState('');
	const [selectedGithubRepo, setSelectedGithubRepo] = useState<any>(null);
	const [showManualRepo, setShowManualRepo] = useState(false);

  useEffect(() => {
    if (selectedCompanyId) {
        axios.get('/api/companies').then(res => {
            const comp = res.data.find((c: any) => c.id === selectedCompanyId);
            if (comp) setCompanyShortName(comp.short_name);
        });
    }
  }, [selectedCompanyId]);

	useEffect(() => {
		(async () => {
			try {
				const [status, repos] = await Promise.all([axios.get('/api/github/status'), axios.get('/api/github/repositories')]);
				setGithubStatus(status.data);
				setGithubRepos(repos.data || []);
			} catch { setGithubStatus({ configured: false }); setGithubRepos([]); }
		})();
	}, []);

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
    setError('');
    setSaving(true);
    try {
      await axios.post('/api/projects', {
        company_id: selectedCompanyId,
        name: formData.name,
        workspace_folder: formData.workspace_folder,
		repository_url: formData.repository_url || undefined,
		github_repository: selectedGithubRepo || undefined,
      });
      setFormData({ name: '', workspace_folder: '', repository_url: '' });
      setIsModalOpen(false);
      fetchProjects();
    } catch (e: any) {
      setError(e.response?.data?.error || 'Failed to create project');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Projects</h1>

      {isOwner && (
        <div className="mb-8">
          <button onClick={() => { setIsModalOpen(true); setError(''); }} className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700">
            Create Project
          </button>
        </div>
      )}

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
        {projects.map(p => (
          <Link key={p.id} to={`./${p.id}`} className="block p-6 bg-white rounded-lg border shadow-sm hover:shadow-md transition">
            <div className="flex items-center mb-4 text-indigo-600">
                <Folder className="mr-2" />
                <h3 className="text-lg font-semibold text-gray-900">{p.name}</h3>
            </div>
            {p.description && <p className="text-sm text-gray-600 mt-2">{p.description}</p>}
            {p.repository_url && (
              <div className="flex items-center mt-2 text-xs text-gray-500">
                <GitBranch className="mr-1 h-3 w-3" />
                <span className="truncate">{p.repository_url}</span>
              </div>
            )}
            <p className="text-xs text-gray-400 mt-4">ID: {p.id}</p>
          </Link>
        ))}
      </div>

      {isModalOpen && (
        <div className="fixed inset-0 bg-black/50 flex justify-center items-center z-50">
          <div className="bg-white p-6 rounded-lg shadow-xl w-full max-w-md" role="dialog">
            <h2 className="text-xl font-bold mb-4">Create Project</h2>
            {error && <div className="mb-4 p-3 bg-red-50 border border-red-200 rounded text-red-700 text-sm">{error}</div>}
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
                <label className="block text-sm font-medium text-gray-700 mb-1">Workspace Folder (Relative to .headcount1/)</label>
                <input type="text" value={formData.workspace_folder} onChange={e => setFormData({...formData, workspace_folder: e.target.value})} className="w-full border rounded p-2 text-sm" />
                <p className="text-xs text-gray-500 mt-1">This directory will be created automatically.</p>
              </div>
              <div>
				<div className="flex items-center justify-between gap-2"><label className="block text-sm font-medium text-gray-700">GitHub repository</label><a href={`/companies/${companyShortName}/mcp-servers`} className="inline-flex items-center gap-1 text-xs text-indigo-700 hover:underline">Manage accounts <ExternalLink size={12}/></a></div>
				{githubStatus?.configured && githubRepos.length > 0 ? <>
					<div className="relative mt-1"><Search size={14} className="absolute left-2 top-2.5 text-gray-400"/><input value={repoSearch} onChange={e => setRepoSearch(e.target.value)} placeholder="Search connected repositories…" className="w-full border rounded p-2 pl-7 text-sm"/></div>
					<select value={selectedGithubRepo?.id || ''} onChange={e => { const repo = githubRepos.find(x => String(x.id) === e.target.value) || null; setSelectedGithubRepo(repo); if (repo) setFormData(f => ({...f, repository_url: repo.clone_url})); }} className="mt-2 w-full border rounded p-2 text-sm"><option value="">Select a connected repository</option>{githubRepos.filter(r => r.full_name.toLowerCase().includes(repoSearch.toLowerCase())).map(r => <option key={r.id} value={r.id}>{r.full_name}</option>)}</select>
				</> : <p className="mt-1 text-xs text-gray-500">Connect GitHub in MCP Servers to choose from permitted repositories.</p>}
				<button type="button" onClick={() => setShowManualRepo(v => !v)} className="mt-2 text-xs text-indigo-700 hover:underline">{showManualRepo ? 'Hide manual repository URL' : 'Use a non-GitHub repository or SSH URL instead'}</button>
				{showManualRepo && <><input type="text" value={formData.repository_url} onChange={e => { setSelectedGithubRepo(null); setFormData({...formData, repository_url: e.target.value}); }} className="mt-2 w-full border rounded p-2 text-sm" placeholder="git@github.com:user/repo.git" /><p className="text-xs text-gray-500 mt-1">For GitLab, Bitbucket, self-hosted Git, or SSH URLs.</p></>}
              </div>
              <div className="flex justify-end space-x-3 pt-4">
                <button type="button" onClick={() => setIsModalOpen(false)} className="text-gray-500 hover:text-gray-700">Cancel</button>
                <button type="submit" disabled={saving} className="bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 disabled:opacity-50">{saving ? 'Creating...' : 'Create'}</button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
};
