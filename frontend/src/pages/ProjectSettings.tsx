/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams, useNavigate } from 'react-router-dom';
import { ArrowLeft, GitBranch, Save } from 'lucide-react';

export const ProjectSettings: React.FC = () => {
  const { shortName, id } = useParams<{ shortName: string; id: string }>();
  const navigate = useNavigate();

  const [project, setProject] = useState<any>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [workspaceFolder, setWorkspaceFolder] = useState('');
  const [repositoryUrl, setRepositoryUrl] = useState('');

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
      } catch (e) {
        setError('Failed to load project');
      } finally {
        setLoading(false);
      }
    };
    fetchProject();
  }, [id]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSuccess('');
    setSaving(true);
    try {
      await axios.put(`/api/projects/${id}`, {
        name,
        description,
        workspace_folder: workspaceFolder,
        repository_url: repositoryUrl || '',
      });
      setSuccess('Project settings saved successfully');
    } catch (e: any) {
      setError(e.response?.data?.error || 'Failed to save project settings');
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return <div className="p-6">Loading...</div>;
  }

  if (error && !project) {
    return (
      <div className="p-6">
        <p className="text-red-600">{error}</p>
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

      {error && (
        <div className="mb-4 p-3 bg-red-50 border border-red-200 rounded text-red-700 text-sm">{error}</div>
      )}
      {success && (
        <div className="mb-4 p-3 bg-green-50 border border-green-200 rounded text-green-700 text-sm">{success}</div>
      )}

      <form onSubmit={handleSave} className="bg-white shadow rounded-lg p-6 space-y-6">
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
          <p className="text-xs text-gray-500 mt-1">Relative to .paperclip2/</p>
        </div>

        <div className="border-t pt-6">
          <div className="flex items-center mb-4">
            <GitBranch className="h-5 w-5 text-gray-500 mr-2" />
            <h2 className="text-lg font-semibold">Git Repository</h2>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Repository URL</label>
            <input
              type="text"
              value={repositoryUrl}
              onChange={e => setRepositoryUrl(e.target.value)}
              className="w-full border rounded p-2 text-sm"
              placeholder="git@github.com:user/repo.git"
            />
            <p className="text-xs text-gray-500 mt-1">
              Must end with .git or be a recognized git hosting URL. Connectivity and authentication will be validated on save.
            </p>
          </div>

          {project?.repository_url && (
            <div className="mt-3 p-3 bg-gray-50 rounded text-sm text-gray-600">
              <span className="font-medium">Current repo:</span> {project.repository_url}
            </div>
          )}
        </div>

        <div className="flex justify-end pt-4">
          <button
            type="submit"
            disabled={saving}
            className="flex items-center bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 disabled:opacity-50"
          >
            <Save className="h-4 w-4 mr-2" />
            {saving ? 'Saving...' : 'Save Settings'}
          </button>
        </div>
      </form>
    </div>
  );
};
