import React, { useState, useEffect, useCallback, useRef } from 'react';
import axios from 'axios';
import { useStore, useIsOwner } from '../store';
import { useLocation, useNavigate } from 'react-router-dom';
import { ExternalLink, GitBranch, KeyRound } from 'lucide-react';

interface BuildVersion {
    /** Version number: "2026.07.29" in production, "staging-<short branch>-<short commit>" on staging. */
    version?: string;
    branch: string;
    commit_hash: string;
    build_date: string;
}

/** "v1.2.3 (branch+date+commit)" — version first, exact build identity after. */
const describeBuild = (b: BuildVersion): string => {
    const build = `${b.branch}+${b.build_date}+${b.commit_hash}`;
    return b.version ? `${b.version} (${build})` : build;
};

interface DeployStatus {
    environment: 'production' | 'staging';
    deploy_source: 'releases' | 'main';
    auto_deploy: boolean;
    current?: BuildVersion;
    deploying?: boolean;
    /** The build an in-progress deploy is switching to. */
    deploy_target?: BuildVersion;
    /** Only returned to the operator (global admin API enabled). */
    last_error?: string;
    /**
     * NAMES of the env vars the last deploy delivered from its GitHub
     * Environment — never the values. Operator-only, like last_error.
     */
    env_key_names?: string[];
    env_updated_at?: string;
}

export const Settings: React.FC = () => {
    const navigate = useNavigate();
    const location = useLocation();
    const { selectedCompanyId, companies, setCompanies, user } = useStore();
    const isOwner = useIsOwner();
    const isAdmin = user?.is_admin === true;

    const [companyShortName, setCompanyShortName] = useState('');
    const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);
    const [deleteConfirmText, setDeleteConfirmText] = useState('');
    const [deleting, setDeleting] = useState(false);

    useEffect(() => {
        const routeShortName = location.pathname.match(/\/companies\/([^/]+)/)?.[1];
        const comp = companies.find(c => c.id === selectedCompanyId)
            ?? companies.find(c => c.short_name === routeShortName);
        if (comp) {
            setCompanyShortName(comp.short_name);
        }
    }, [selectedCompanyId, companies, location.pathname]);

    const [basePath, setBasePath] = useState('');
    const [deploySource, setDeploySource] = useState<'releases' | 'main'>('releases');
    const [autoDeploy, setAutoDeploy] = useState(true);
    const [saving, setSaving] = useState(false);
    const [sshKey, setSshKey] = useState('');
    const [sshFileName, setSshFileName] = useState('');
    const sshFileInputRef = useRef<HTMLInputElement>(null);

    const [deployStatus, setDeployStatus] = useState<DeployStatus | null>(null);

    useEffect(() => {
        const fetchSettings = async () => {
            try {
                const res = await axios.get('/api/settings');
                if (res.data) {
                    setBasePath(res.data.base_path || '');
                    setDeploySource(res.data.deploy_source === 'main' ? 'main' : 'releases');
                    setAutoDeploy(res.data.auto_deploy !== false);
                }
            } catch (e) {
                console.error(e);
            }
        };
        fetchSettings();
    }, []);

    const fetchDeployStatus = useCallback(async () => {
        if (!isAdmin) {
            setDeployStatus(null);
            return;
        }
        try {
            const res = await axios.get('/api/deploy/status');
            setDeployStatus(res.data);
        } catch (e) {
            console.error(e);
        }
    }, [isAdmin]);

    useEffect(() => {
        fetchDeployStatus();
    }, [fetchDeployStatus]);

    const handleSave = async (e: React.FormEvent) => {
        e.preventDefault();
        setSaving(true);
        try {
            // The SSH key is per-user, encrypted at rest under your passkey.
            if (sshKey) {
                await axios.post('/api/settings/ssh', { key: sshKey });
                setSshKey('');
            }

            // The workspace root and deploy config are instance-global and only
            // the first registered user can change them.
            if (isAdmin) {
                await axios.post('/api/settings', {
                    base_path: basePath,
                    deploy_source: deploySource,
                    auto_deploy: autoDeploy,
                });
            }

            const routeShortName = location.pathname.match(/\/companies\/([^/]+)/)?.[1];
            const currentCompany = companies.find(c => c.id === selectedCompanyId)
                ?? companies.find(c => c.short_name === routeShortName);
            if (currentCompany && companyShortName !== currentCompany.short_name) {
                await axios.put(`/api/companies/${currentCompany.id}`, { short_name: companyShortName });
                const updatedCompanies = companies.map(c =>
                    c.id === selectedCompanyId ? { ...c, short_name: companyShortName } : c
                );
                setCompanies(updatedCompanies);
                navigate(`/companies/${companyShortName}/settings`, { replace: true });
            }

            alert('Settings saved!');
        } catch (e) {
            console.error(e);
            alert('Failed to save settings');
        } finally {
            setSaving(false);
        }
    };

    const handleSaveDeployment = async () => {
        setSaving(true);
        try {
            await axios.post('/api/settings', {
                deploy_source: deploySource,
                auto_deploy: autoDeploy,
            });
            alert('Deployment settings saved!');
        } catch (e) {
            console.error(e);
            alert('Failed to save deployment settings');
        } finally {
            setSaving(false);
        }
    };

    const handleSshFileUpload = (e: React.ChangeEvent<HTMLInputElement>) => {
        const file = e.target.files?.[0];
        if (!file) return;
        setSshFileName(file.name);
        const reader = new FileReader();
        reader.onload = ev => setSshKey((ev.target?.result as string) || '');
        reader.readAsText(file);
    };

    const handleDeleteCompany = async () => {
        const currentCompany = companies.find(c => c.id === selectedCompanyId);
        if (!currentCompany || deleteConfirmText !== currentCompany.short_name) {
            return;
        }

        setDeleting(true);
        try {
            await axios.delete(`/api/companies/${currentCompany.id}`);
            const updatedCompanies = companies.filter(c => c.id !== selectedCompanyId);
            setCompanies(updatedCompanies);

            if (updatedCompanies.length > 0) {
                navigate(`/companies/${updatedCompanies[0].short_name}`, { replace: true });
            } else {
                navigate('/add-company', { replace: true });
            }
        } catch (e) {
            console.error(e);
            alert('Failed to delete company');
        } finally {
            setDeleting(false);
            setShowDeleteConfirm(false);
            setDeleteConfirmText('');
        }
    };

    return (
        <div className="max-w-2xl">
            <h1 className="text-2xl font-bold mb-6">Settings</h1>

            <div className="bg-white p-6 rounded-lg shadow-sm border">
                <form onSubmit={handleSave} className="space-y-4">
                    <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4">Company Settings</h2>
                    <div>
                        <label className="block text-sm font-medium text-gray-700 mb-1">
                            Company Short Name
                        </label>
                        <p className="text-xs text-gray-500 mb-3">
                            Used as a prefix for Agent CLI runs. Max 2 characters.
                        </p>
	                            <input
                            type="text"
                            maxLength={2}
                            value={companyShortName}
                            onChange={e => setCompanyShortName(e.target.value.toLowerCase())}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border uppercase font-mono"
                            placeholder="ac"
                        />
                    </div>

                    <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4 mt-8">Global Settings</h2>
                    <div>
                        <label className="block text-sm font-medium text-gray-700 mb-1">
                            Workspace Root Directory
                        </label>
                        <input
                            type="text"
                            value={basePath}
                            onChange={e => setBasePath(e.target.value)}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border"
                            placeholder="/home/user/.headcount1"
                        />
                    </div>
					<div className="flex gap-3 rounded-lg border border-indigo-100 bg-indigo-50 p-4 text-sm text-indigo-950">
						<GitBranch className="mt-0.5 shrink-0 text-indigo-700" size={18}/>
						<p><span className="font-semibold">Connect GitHub in MCP Servers.</span> Add personal and work GitHub accounts separately, then choose from their permitted repositories when setting up a project. <a href={`/companies/${companyShortName}/mcp-servers`} className="inline-flex items-center gap-1 font-medium text-indigo-700 underline hover:text-indigo-900">Manage GitHub accounts <ExternalLink size={13}/></a></p>
					</div>
                    <div className="bg-indigo-50 border border-indigo-100 rounded-md p-3 text-sm text-indigo-900">
                        Models used for lightweight internal calls (commit messages, artifact Q&A) are configured under <strong>Default Models</strong> on the{' '}
                        <a href={`/companies/${companyShortName}/providers`} className="underline hover:text-indigo-700">LLM Providers</a> page.
                    </div>
					<details className="rounded-lg border border-gray-200 p-4">
						<summary className="flex cursor-pointer list-none items-center gap-2 text-sm font-medium text-gray-700"><KeyRound size={16}/> Advanced: SSH key for non-GitHub repositories</summary>
						<p className="mt-3 text-xs text-gray-500 mb-2">Only use this for GitLab, Bitbucket, self-hosted Git, or a manually entered SSH URL. GitHub repositories connected above do not use this key.</p>
						<textarea
                            value={sshKey}
                            onChange={e => { setSshKey(e.target.value); setSshFileName(''); }}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border font-mono"
                            rows={4}
                            placeholder="-----BEGIN OPENSSH PRIVATE KEY-----..."
                        />
                        <div className="mt-2 flex items-center gap-3">
                            <button
                                type="button"
                                onClick={() => sshFileInputRef.current?.click()}
                                className="text-sm text-indigo-600 hover:text-indigo-800 border border-indigo-300 rounded px-3 py-1"
                            >
                                Upload from file
                            </button>
                            {sshFileName && (
                                <span className="text-xs text-gray-500 font-mono">{sshFileName}</span>
                            )}
                            <input
                                ref={sshFileInputRef}
                                type="file"
                                accept=".pem,.key,*"
                                className="hidden"
	                                onChange={handleSshFileUpload}
							/>
						</div>
					</details>

                    <div className="flex gap-4">
                        <button
                            type="submit"
                            disabled={saving}
                            className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700 disabled:bg-indigo-400"
                        >
                            {saving ? 'Saving...' : 'Save Settings'}
                        </button>

                        {isOwner && (
                            <button
                                type="button"
                                onClick={() => navigate(`/companies/${companyShortName}/backup`)}
                                className="bg-blue-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-blue-700"
                            >
                                Export & Import
                            </button>
                        )}
                    </div>
                </form>
            </div>

            {isAdmin && deployStatus && deployStatus.environment !== 'staging' && <div className="bg-white p-6 rounded-lg shadow-sm border mt-8">
                <div className="flex items-center justify-between border-b pb-2 mb-4">
                    <h2 className="text-lg font-medium text-gray-900">Deployment</h2>
                    {deployStatus && (
                        <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${deployStatus.environment === 'production' ? 'bg-emerald-100 text-emerald-800' : 'bg-amber-100 text-amber-800'}`}>
                            {deployStatus.environment}
                        </span>
                    )}
                </div>

                {deployStatus?.current && (
                    <div className="mb-4 text-sm text-gray-600 space-y-1">
                        <div className="flex items-baseline gap-2">
                            <span className="font-medium">Version</span>
                            <span className="text-base font-semibold text-gray-900 font-mono">
                                {deployStatus.current.version || 'dev'}
                            </span>
                        </div>
                        <div className="text-xs text-gray-500">
                            Build{' '}
                            <code className="bg-gray-100 px-1 rounded">
                                {deployStatus.current.branch}+{deployStatus.current.build_date}+{deployStatus.current.commit_hash}
                            </code>
                        </div>
                        {deployStatus.deploying && (
                            <div className="text-xs text-indigo-600">
                                Deploying
                                {deployStatus.deploy_target && (
                                    <> to <code className="bg-indigo-50 px-1 rounded">
                                        {describeBuild(deployStatus.deploy_target)}
                                    </code></>
                                )}
                                {' '}— in-flight runs are draining, then the server restarts.
                            </div>
                        )}
                        {deployStatus.last_error && (
                            <div className="text-xs text-red-600">Last deploy error: {deployStatus.last_error}</div>
                        )}
                        {/* Names only. Enough to confirm configuration arrived without
                            shell access to the box; the values stay on the server. */}
                        {deployStatus.env_key_names && deployStatus.env_key_names.length > 0 && (
                            <div className="text-xs text-gray-500">
                                Config delivered from GitHub
                                {deployStatus.env_updated_at && ` on ${new Date(deployStatus.env_updated_at).toLocaleString()}`}:{' '}
                                <span className="font-mono">{deployStatus.env_key_names.join(', ')}</span>
                            </div>
                        )}
                    </div>
                )}

                <p className="text-xs text-gray-500 mb-4">
                    New builds are deployed to this server automatically by CI. Production servers apply
                    updates from the source selected below; staging servers deploy any branch/PR pushed to them.
                    Each deploy also delivers every variable and secret from its GitHub Environment
                    (Settings → Environments), so this server's env vars are managed there rather than
                    on the box. Names that could let a value execute code (<code>PATH</code>,{' '}
                    <code>LD_*</code>, …) are dropped and reported back to the deploy job.
                </p>

                <div className="space-y-4">
                    <div>
                        <label className="block text-sm font-medium text-gray-700 mb-1">
                            Update source
                        </label>
                        <p className="text-xs text-gray-500 mb-2">
                            Which builds a production server auto-deploys. (Ignored on staging.)
                        </p>
                        <select
                            value={deploySource}
                            onChange={e => setDeploySource(e.target.value === 'main' ? 'main' : 'releases')}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm p-2 border bg-white"
                        >
                            <option value="releases">Releases (recommended)</option>
                            <option value="main">Main branch</option>
                        </select>
                    </div>

                    <div className="flex items-center gap-3">
                        <input
                            type="checkbox"
                            id="auto_deploy"
                            checked={autoDeploy}
                            onChange={e => setAutoDeploy(e.target.checked)}
                            className="h-4 w-4 text-indigo-600 border-gray-300 rounded"
                        />
                        <label htmlFor="auto_deploy" className="text-sm text-gray-700">
                            Auto-deploy matching builds (uncheck to pause deployments on this server)
                        </label>
                    </div>
                </div>

                <div className="mt-6 flex justify-end">
                    <button
                        type="button"
                        onClick={handleSaveDeployment}
                        disabled={saving}
                        className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700 disabled:bg-indigo-400"
                    >
                        {saving ? 'Saving...' : 'Save Deployment Settings'}
                    </button>
                </div>
            </div>}

            {isOwner && <div className="bg-white p-6 rounded-lg shadow-sm border border-red-200 mt-8">
                <h2 className="text-lg font-medium text-red-600 border-b border-red-200 pb-2 mb-4">Danger Zone</h2>
                <div className="flex items-center justify-between">
                    <div>
                        <h3 className="text-sm font-medium text-gray-900">Delete this company</h3>
                        <p className="text-xs text-gray-500 mt-1">
                            Once deleted, it will be gone forever. All data will be archived before deletion.
                        </p>
                    </div>
                    <button
                        type="button"
                        onClick={() => setShowDeleteConfirm(true)}
                        className="bg-red-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-red-700"
                    >
                        Delete Company
                    </button>
                </div>
            </div>}

            {showDeleteConfirm && (
                <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
                    <div className="bg-white rounded-lg p-6 max-w-md w-full mx-4">
                        <h3 className="text-lg font-bold text-red-600 mb-4">Delete Company</h3>
                        <div className="bg-red-50 border border-red-200 rounded-md p-4 mb-4">
                            <p className="text-sm text-red-800 font-medium mb-2">
                                Warning: This action cannot be undone!
                            </p>
                            <p className="text-sm text-red-700">
                                This will permanently delete the company and all associated data including:
                            </p>
                            <ul className="text-sm text-red-700 list-disc list-inside mt-2">
                                <li>All projects and tasks</li>
                                <li>All agents and their configurations</li>
                                <li>All comments and run logs</li>
                                <li>All workspace files</li>
                            </ul>
                            <p className="text-sm text-red-700 mt-2">
                                Your files will be archived before deletion, but the company data cannot be restored.
                            </p>
                        </div>
                        <p className="text-sm text-gray-700 mb-2">
                            To confirm, type the company short name: <span className="font-mono font-bold">{companyShortName}</span>
                        </p>
                        <input
                            type="text"
                            value={deleteConfirmText}
                            onChange={e => setDeleteConfirmText(e.target.value)}
                            className="w-full border-gray-300 rounded-md shadow-sm focus:ring-red-500 focus:border-red-500 sm:text-sm p-2 border mb-4"
                            placeholder={companyShortName}
                        />
                        <div className="flex gap-3 justify-end">
                            <button
                                type="button"
                                onClick={() => {
                                    setShowDeleteConfirm(false);
                                    setDeleteConfirmText('');
                                }}
                                className="px-4 py-2 border border-gray-300 rounded-md text-gray-700 hover:bg-gray-50"
                            >
                                Cancel
                            </button>
                            <button
                                type="button"
                                onClick={handleDeleteCompany}
                                disabled={deleteConfirmText !== companyShortName || deleting}
                                className="bg-red-600 text-white px-4 py-2 rounded-md hover:bg-red-700 disabled:bg-red-300 disabled:cursor-not-allowed"
                            >
                                {deleting ? 'Deleting...' : 'Delete Company'}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};
