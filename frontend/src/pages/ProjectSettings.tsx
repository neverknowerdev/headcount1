/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams, useNavigate } from 'react-router-dom';
import { ArrowLeft, GitBranch, Save, CheckCircle2, AlertCircle, Loader2, ExternalLink, Search, Pencil } from 'lucide-react';

export const ProjectSettings: React.FC = () => {
  const { shortName, id } = useParams<{ shortName: string; id: string }>();
  const navigate = useNavigate();

  const [project, setProject] = useState<any>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [workspaceFolder, setWorkspaceFolder] = useState('');
  const [detailsError, setDetailsError] = useState('');
  const [detailsSuccess, setDetailsSuccess] = useState('');
  const [savingDetails, setSavingDetails] = useState(false);

  const [repositoryUrl, setRepositoryUrl] = useState('');
	const [githubRepos, setGithubRepos] = useState<any[]>([]);
	const [githubStatus, setGithubStatus] = useState<any>(null);
	const [repoSearch, setRepoSearch] = useState('');
  const [selectedGithubRepo, setSelectedGithubRepo] = useState<any>(null);
	const [editingGithubRepo, setEditingGithubRepo] = useState(false);
	const [showManualRepo, setShowManualRepo] = useState(false);
  const [repoError, setRepoError] = useState('');
  const [repoSuccess, setRepoSuccess] = useState('');
  const [savingRepo, setSavingRepo] = useState(false);

  const [codegraphStatus, setCodegraphStatus] = useState<string | null>(null);

  useEffect(() => {
    const fetchProject = async () => {
      try {
        const res = await axios.get(`/api/projects/${id}`);
        const p = res.data;
        setProject(p);
        setName(p.name || '');
        setDescription(p.description || '');
        setWorkspaceFolder(p.workspace_folder || '');
        setRepositoryUrl(p.repository_url || '');
		// Preserve the editable manual field for projects that already use a
		// non-GitHub repository. New projects still lead with the GitHub picker.
		setShowManualRepo(Boolean(p.repository_url && !p.github_repository_id));
      } catch {
        setLoadError('Failed to load project');
      } finally {
        setLoading(false);
      }
    };
    fetchProject();
  }, [id]);

  const fetchCodegraphStatus = useCallback(async () => {
    try {
      const res = await axios.get(`/api/projects/${id}/codegraph`);
      setCodegraphStatus(res.data.init_status ?? '');
    } catch {
      setCodegraphStatus(null);
    }
  }, [id]);

  useEffect(() => { fetchCodegraphStatus(); }, [fetchCodegraphStatus]);

  useEffect(() => { (async () => { try { const [status, repos] = await Promise.all([axios.get('/api/github/status'), axios.get('/api/github/repositories')]); setGithubStatus(status.data); setGithubRepos(repos.data || []); } catch { /* GitHub is optional */ } })(); }, []);

	useEffect(() => {
		if (!project?.github_repository_id || githubRepos.length === 0) return;
		const connected = githubRepos.find(repo => String(repo.id) === String(project.github_repository_id));
		if (connected) setSelectedGithubRepo(connected);
	}, [project, githubRepos]);

  // Poll every 3 s while initializing
  useEffect(() => {
    if (codegraphStatus !== 'initializing') return;
    const t = setInterval(fetchCodegraphStatus, 3000);
    return () => clearInterval(t);
  }, [codegraphStatus, fetchCodegraphStatus]);

  // Re-check codegraph after a successful repo save
  useEffect(() => {
    if (repoSuccess) fetchCodegraphStatus();
  }, [repoSuccess, fetchCodegraphStatus]);

  const handleSaveDetails = async (e: React.FormEvent) => {
    e.preventDefault();
    setDetailsError('');
    setDetailsSuccess('');
    setSavingDetails(true);
    try {
      await axios.put(`/api/projects/${id}`, {
        name,
        description,
        workspace_folder: workspaceFolder,
      });
      setDetailsSuccess('Project details saved');
    } catch (e: any) {
      setDetailsError(e.response?.data?.error || 'Failed to save project details');
    } finally {
      setSavingDetails(false);
    }
  };

  const handleSaveRepo = async (e: React.FormEvent) => {
    e.preventDefault();
    setRepoError('');
    setRepoSuccess('');
    if (!repositoryUrl.trim()) {
      setRepoError('Repository URL is required');
      return;
    }
    setSavingRepo(true);
    try {
      const res = await axios.put(`/api/projects/${id}`, {
        name,
        repository_url: repositoryUrl.trim(),
		github_repository: selectedGithubRepo || undefined,
      });
      setProject(res.data);
      setRepositoryUrl(res.data.repository_url || repositoryUrl.trim());
      setRepoSuccess('Repository connected successfully');
		setEditingGithubRepo(false);
		setShowManualRepo(false);
    } catch (e: any) {
      setRepoError(e.response?.data?.error || 'Failed to connect repository');
    } finally {
      setSavingRepo(false);
    }
  };

  if (loading) {
    return <div className="p-6">Loading...</div>;
  }

  if (loadError) {
    return (
      <div className="p-6">
        <p className="text-red-600">{loadError}</p>
        <button onClick={() => navigate(`/companies/${shortName}/projects`)} className="mt-4 text-indigo-600 hover:underline">
          Back to Projects
        </button>
      </div>
    );
  }

	const filteredGithubRepos = githubRepos.filter(repo => repo.full_name.toLowerCase().includes(repoSearch.trim().toLowerCase()));
	const connectedGithubRepo = Boolean(project?.github_repository_id && project?.repository_url);
	const connectedRepoName = selectedGithubRepo?.full_name || project?.repository_url?.replace(/^https:\/\/github\.com\//, '').replace(/\.git$/, '') || '';

  return (
    <div className="max-w-2xl mx-auto">
      <div className="flex items-center mb-6">
        <button
          onClick={() => navigate(`/companies/${shortName}/projects`)}
          className="mr-4 text-gray-500 hover:text-gray-700"
        >
          <ArrowLeft className="h-5 w-5" />
        </button>
        <h1 className="text-2xl font-bold">Project Settings</h1>
      </div>

      {/* Project details section */}
      <form onSubmit={handleSaveDetails} className="bg-white shadow rounded-lg p-6 space-y-4 mb-6">
        <h2 className="text-base font-semibold text-gray-900">Project Details</h2>

        {detailsError && (
          <div className="p-3 bg-red-50 border border-red-200 rounded text-red-700 text-sm">{detailsError}</div>
        )}
        {detailsSuccess && (
          <div className="p-3 bg-green-50 border border-green-200 rounded text-green-700 text-sm">{detailsSuccess}</div>
        )}

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Project Name</label>
          <input
            type="text"
            value={name}
            onChange={e => setName(e.target.value)}
            required
            className="w-full border rounded p-2"
          />
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
          <textarea
            value={description}
            onChange={e => setDescription(e.target.value)}
            rows={3}
            className="w-full border rounded p-2"
            placeholder="Project description..."
          />
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Workspace Folder</label>
          <input
            type="text"
            value={workspaceFolder}
            onChange={e => setWorkspaceFolder(e.target.value)}
            className="w-full border rounded p-2 text-sm"
          />
          <p className="text-xs text-gray-500 mt-1">Relative to .headcount1/</p>
        </div>

        <div className="flex justify-end">
          <button
            type="submit"
            disabled={savingDetails}
            className="flex items-center bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 disabled:opacity-50"
          >
            <Save className="h-4 w-4 mr-2" />
            {savingDetails ? 'Saving...' : 'Save'}
          </button>
        </div>
      </form>

      {/* Git repository section — separate card with its own save */}
      <form onSubmit={handleSaveRepo} className="bg-white shadow rounded-lg p-6 space-y-4">
        <div className="flex items-center gap-2">
          <GitBranch className="h-5 w-5 text-gray-500" />
          <h2 className="text-base font-semibold text-gray-900">Git Repository</h2>
        </div>

        {repoError && (
          <div className="p-3 bg-red-50 border border-red-200 rounded text-red-700 text-sm">{repoError}</div>
        )}
        {repoSuccess && (
          <div className="p-3 bg-green-50 border border-green-200 rounded text-green-700 text-sm">{repoSuccess}</div>
        )}

			{connectedGithubRepo && !editingGithubRepo ? (
				<div className="rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
					<div className="flex items-center justify-between gap-4">
						<div className="flex min-w-0 items-center gap-3"><span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-gray-900 text-white"><GitBranch size={21}/></span><div className="min-w-0"><div className="flex items-center gap-2"><p className="truncate font-semibold text-gray-900">{connectedRepoName}</p><span className="inline-flex items-center gap-1 rounded-full bg-green-50 px-2 py-0.5 text-xs font-medium text-green-700"><CheckCircle2 size={12}/> Connected</span></div><p className="mt-0.5 truncate text-xs text-gray-500">{project.repository_url}</p></div></div>
						<button type="button" onClick={() => { setEditingGithubRepo(true); setRepoSuccess(''); }} className="inline-flex shrink-0 items-center gap-1.5 rounded-lg border border-gray-200 px-3 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"><Pencil size={14}/> Change</button>
					</div>
				</div>
			) : (
				<div className="overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm">
					<div className="flex items-start justify-between gap-3 border-b border-gray-100 p-4"><div className="flex gap-3"><span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-gray-900 text-white"><GitBranch size={19}/></span><div><h3 className="font-semibold text-gray-900">Choose a GitHub repository</h3><p className="mt-0.5 text-xs text-gray-500">Repositories available to your connected GitHub accounts.</p></div></div><a href={`/companies/${shortName}/mcp-servers`} className="inline-flex shrink-0 items-center gap-1 text-xs font-medium text-indigo-700 hover:underline">Manage accounts <ExternalLink size={12}/></a></div>
					{githubStatus?.configured && githubRepos.length > 0 ? <div className="p-4"><div className="relative"><Search className="absolute left-3 top-2.5 text-gray-400" size={17}/><input value={repoSearch} onChange={e => setRepoSearch(e.target.value)} placeholder="Search by owner or repository name" className="w-full rounded-lg border border-gray-200 py-2 pl-9 pr-3 text-sm outline-none focus:border-indigo-400 focus:ring-2 focus:ring-indigo-100"/></div><div className="mt-2 max-h-56 overflow-y-auto rounded-lg border border-gray-100">{filteredGithubRepos.map(repo => <button type="button" key={repo.id} onClick={() => { setSelectedGithubRepo(repo); setRepositoryUrl(repo.clone_url); }} className={`flex w-full items-center justify-between border-b border-gray-100 px-3 py-2.5 text-left text-sm last:border-0 ${String(selectedGithubRepo?.id) === String(repo.id) ? 'bg-indigo-50 text-indigo-800' : 'hover:bg-gray-50'}`}><span className="truncate font-medium">{repo.full_name}</span>{String(selectedGithubRepo?.id) === String(repo.id) && <CheckCircle2 className="shrink-0 text-indigo-600" size={16}/>}</button>)}{filteredGithubRepos.length === 0 && <p className="px-3 py-6 text-center text-sm text-gray-500">No repositories match “{repoSearch}”.</p>}</div></div> : <p className="p-4 text-sm text-gray-600">{githubStatus?.configured ? 'No permitted repositories found. Add a GitHub account or update repository access in GitHub.' : 'GitHub integration is unavailable on this environment.'}</p>}
				</div>
			)}
			<div className="flex items-center justify-between"><button type="button" onClick={() => { const opening = !showManualRepo; setShowManualRepo(opening); if (opening) { setSelectedGithubRepo(null); setEditingGithubRepo(false); } }} className="text-sm font-medium text-gray-500 hover:text-gray-800">{showManualRepo ? 'Hide manual repository URL' : 'Use SSH or another Git provider'}</button>{editingGithubRepo && <button type="button" onClick={() => { setEditingGithubRepo(false); setSelectedGithubRepo(githubRepos.find(repo => String(repo.id) === String(project?.github_repository_id)) || null); setRepositoryUrl(project?.repository_url || ''); }} className="text-sm text-gray-500 hover:text-gray-800">Cancel change</button>}</div>
			{showManualRepo && <div className="mt-3">
          <label className="block text-sm font-medium text-gray-700 mb-1">Repository URL</label>
          <input
            type="text"
            value={repositoryUrl}
            onChange={e => setRepositoryUrl(e.target.value)}
            className="w-full border rounded p-2 text-sm font-mono"
            placeholder="git@github.com:user/repo.git"
          />
          <p className="text-xs text-gray-500 mt-1">
            Enter a URL like <code>github.com/user/repo</code> or <code>git@github.com:user/repo.git</code>.
			For GitLab, Bitbucket, self-hosted Git, or local repositories.
          </p>
			</div>}

        {codegraphStatus !== null && (
          <div className="flex items-center gap-2 text-sm">
            <span className="text-gray-500">Codegraph:</span>
            {codegraphStatus === 'ready' ? (
              <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-green-100 text-green-700">
                <CheckCircle2 size={11} /> Ready
              </span>
            ) : codegraphStatus === 'initializing' ? (
              <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-amber-100 text-amber-700">
                <Loader2 size={11} className="animate-spin" /> Initializing…
              </span>
            ) : codegraphStatus?.startsWith('error:') ? (
              <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-red-100 text-red-700" title={codegraphStatus}>
                <AlertCircle size={11} /> Init failed
              </span>
            ) : (
              <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-500">
                <AlertCircle size={11} /> Not initialized
              </span>
            )}
          </div>
        )}

		{(!connectedGithubRepo || editingGithubRepo || showManualRepo) && <div className="flex justify-end">
          <button
            type="submit"
            disabled={savingRepo}
            className="flex items-center bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 disabled:opacity-50"
          >
            <GitBranch className="h-4 w-4 mr-2" />
			{savingRepo ? 'Connecting...' : connectedGithubRepo ? 'Update Repository' : 'Connect Repository'}
          </button>
		</div>}
      </form>
    </div>
  );
};
