/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams, useNavigate } from 'react-router-dom';
import { ArrowLeft, GitBranch, Save, CheckCircle2, AlertCircle, Loader2 } from 'lucide-react';
import { ProjectEnvironments } from '../components/ProjectEnvironments';

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
      });
      setProject(res.data);
      setRepositoryUrl(res.data.repository_url || repositoryUrl.trim());
      setRepoSuccess('Repository connected successfully');
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

        <div>
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
            The server will connect via SSH — make sure your SSH key is configured in Settings.
          </p>
        </div>

        {project?.repository_url && (
          <div className="p-3 bg-gray-50 rounded text-sm text-gray-600 font-mono break-all">
            {project.repository_url}
          </div>
        )}

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

        <div className="flex justify-end">
          <button
            type="submit"
            disabled={savingRepo}
            className="flex items-center bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 disabled:opacity-50"
          >
            <GitBranch className="h-4 w-4 mr-2" />
            {savingRepo ? 'Connecting...' : 'Connect Repository'}
          </button>
        </div>
      </form>

      {/* Deploy environments: secrets/variables + Vercel/GitHub connectors */}
      {project && <ProjectEnvironments projectId={project.id} />}
    </div>
  );
};
