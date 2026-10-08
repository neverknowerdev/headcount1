import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { ArrowLeft, Save } from 'lucide-react';
import { UsagePanel } from '../components/UsagePanel';
import { errorMessage, formatDateTime } from '../lib/workflow';

interface Agent {
    id: number;
    company_id: number;
    name: string;
    role_key: string;
    short_name: string;
    description: string;
    system_prompt: string;
    builtin: boolean;
    enabled: boolean;
}

interface AgentRun {
    id: number;
    task_id: number;
    name?: string;
    title?: string;
    status: string;
    started_at?: string;
}

interface RoleConfig {
    name: string;
    prompt: string;
}

// An agent is a role: a name, a description and a short prompt that says who
// is speaking. Which model runs and which tools are available belong to the
// task and its workflow, not to the agent.
export const AgentDetails: React.FC = () => {
    const { id, shortName } = useParams<{ id: string; shortName: string }>();
    const [agent, setAgent] = useState<Agent | null>(null);
    const [roles, setRoles] = useState<RoleConfig[]>([]);
    const [runs, setRuns] = useState<AgentRun[]>([]);
    const [activeTab, setActiveTab] = useState('role');
    const [form, setForm] = useState({ name: '', role_key: '', short_name: '', description: '', system_prompt: '' });
    const [saveState, setSaveState] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');
    const [saveError, setSaveError] = useState<string | null>(null);

    // applyAgent puts a loaded agent on the page and into the form.
    const applyAgent = useCallback((loaded: Agent) => {
        setAgent(loaded);
        setForm({
            name: loaded.name,
            role_key: loaded.role_key || '',
            short_name: loaded.short_name || '',
            description: loaded.description || '',
            system_prompt: loaded.system_prompt || '',
        });
    }, []);

    useEffect(() => {
        let current = true;
        Promise.all([axios.get(`/api/agents/${id}`), axios.get('/api/agent-configs'), axios.get(`/api/agents/${id}/runs`)])
            .then(([agentRes, rolesRes, runsRes]) => {
                if (!current) return;
                applyAgent(agentRes.data);
                setRoles(rolesRes.data || []);
                setRuns(runsRes.data || []);
            })
            .catch(e => console.error(e));
        return () => { current = false; };
    }, [id, applyAgent]);

    const save = async (values: typeof form) => {
        setSaveState('saving');
        setSaveError(null);
        try {
            const res = await axios.put(`/api/agents/${id}`, values);
            applyAgent(res.data);
            setSaveState('saved');
            setTimeout(() => setSaveState('idle'), 2000);
        } catch (e) {
            setSaveError(errorMessage(e, 'Save failed'));
            setSaveState('error');
        }
    };

    if (!agent) return <div>Loading...</div>;

    const defaultPrompt = agent.builtin ? roles.find(role => role.name === agent.role_key || role.name === agent.name)?.prompt : undefined;

    return (
        <div className="flex h-full flex-col">
            <div className="mb-6 flex items-center space-x-4">
                <Link to={`/companies/${shortName}/agents`} className="text-gray-500 hover:text-gray-900"><ArrowLeft size={20} /></Link>
                <h1 className="text-2xl font-bold">{agent.name}</h1>
                {agent.builtin && <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-600">built-in role</span>}
            </div>

            <div className="mb-6 border-b">
                <nav className="-mb-px flex space-x-8">
                    {[
                        { key: 'role', label: 'Role' },
                        { key: 'usage', label: 'Usage' },
                        { key: 'sessions', label: 'Sessions' },
                    ].map(({ key, label }) => (
                        <button
                            key={key}
                            onClick={() => setActiveTab(key)}
                            className={`${activeTab === key ? 'border-indigo-500 text-indigo-600' : 'border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700'} whitespace-nowrap border-b-2 px-1 py-4 text-sm font-medium`}
                        >
                            {label}
                        </button>
                    ))}
                </nav>
            </div>

            <div className="flex-1 overflow-y-auto">
                {activeTab === 'role' && (
                    <form
                        onSubmit={e => { e.preventDefault(); save(form); }}
                        className="max-w-2xl space-y-4 rounded-lg border bg-white p-6 shadow"
                        data-testid="agent-role"
                    >
                        <p className="text-sm text-gray-500">
                            An agent is a role in the workflow. Its prompt only says who is speaking; what to do, with which model and which tools, comes from the task.
                        </p>
                        <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
                            <div>
                                <label className="mb-1 block text-sm font-medium text-gray-700">Name</label>
                                <input type="text" required value={form.name} disabled={agent.builtin} onChange={e => setForm({ ...form, name: e.target.value })} className="w-full rounded border p-2 disabled:bg-gray-100 disabled:text-gray-500" />
                            </div>
                            <div>
                                <label className="mb-1 block text-sm font-medium text-gray-700">Role</label>
                                <input type="text" value={form.role_key} disabled={agent.builtin} onChange={e => setForm({ ...form, role_key: e.target.value })} className="w-full rounded border p-2 disabled:bg-gray-100 disabled:text-gray-500" placeholder="e.g. CTO" />
                            </div>
                            <div>
                                <label className="mb-1 block text-sm font-medium text-gray-700">Short name</label>
                                <input type="text" value={form.short_name} disabled={agent.builtin} onChange={e => setForm({ ...form, short_name: e.target.value })} className="w-full rounded border p-2 disabled:bg-gray-100 disabled:text-gray-500" placeholder="e.g. CTO" />
                            </div>
                        </div>
                        <div>
                            <label className="mb-1 block text-sm font-medium text-gray-700">Description</label>
                            <input type="text" value={form.description} onChange={e => setForm({ ...form, description: e.target.value })} className="w-full rounded border p-2" />
                        </div>
                        <div>
                            <label htmlFor="agent-prompt" className="mb-1 block text-sm font-medium text-gray-700">Prompt</label>
                            <textarea id="agent-prompt" required rows={6} value={form.system_prompt} onChange={e => setForm({ ...form, system_prompt: e.target.value })} className="w-full rounded border p-2 font-mono text-sm" />
                            <p className="mt-1 text-xs text-gray-500">
                                Starts as one line. Add what this role should always know about your company: its product, its standards, its tone.
                            </p>
                        </div>
                        <div className="flex items-center gap-3 pt-2">
                            <button type="submit" disabled={saveState === 'saving'} className="flex items-center rounded bg-indigo-600 px-4 py-2 text-white hover:bg-indigo-700 disabled:opacity-60">
                                <Save size={16} className="mr-2" /> {saveState === 'saving' ? 'Saving…' : 'Save Changes'}
                            </button>
                            {defaultPrompt && form.system_prompt !== defaultPrompt && (
                                <button type="button" onClick={() => save({ ...form, system_prompt: defaultPrompt })} disabled={saveState === 'saving'} className="rounded border border-gray-300 px-4 py-2 text-sm text-gray-700 hover:bg-gray-50 disabled:opacity-60">
                                    Restore default
                                </button>
                            )}
                            {saveState === 'saved' && <span className="text-sm text-green-600">Saved</span>}
                            {saveState === 'error' && <span className="text-sm text-red-600">{saveError}</span>}
                        </div>
                    </form>
                )}

                {activeTab === 'usage' && (
                    <UsagePanel
                        reportUrl={`/api/usage?company_id=${agent.company_id}&agent_id=${agent.id}&group_by=root_task,phase,phase_tier,model`}
                        callsQuery={`company_id=${agent.company_id}&agent_id=${agent.id}`}
                        dimensions={['root_task', 'phase', 'model']}
                    />
                )}

                {activeTab === 'sessions' && (
                    <div className="overflow-hidden rounded-lg border bg-white">
                        {runs.length === 0 ? (
                            <p className="p-4 text-sm italic text-gray-500">This role has not run an executor session yet.</p>
                        ) : (
                            runs.map(run => (
                                <Link key={run.id} to={`/companies/${shortName}/run-logs/${run.id}`} className="flex items-center gap-3 border-t border-gray-100 px-4 py-2 text-sm first:border-t-0 hover:bg-gray-50">
                                    <span className="font-mono text-xs text-gray-500">{run.name || `#${run.id}`}</span>
                                    <span className="min-w-0 flex-1 truncate">{run.title || `Task #${run.task_id}`}</span>
                                    <span className="text-xs text-gray-500">{run.status}</span>
                                    <span className="text-xs text-gray-400">{formatDateTime(run.started_at)}</span>
                                </Link>
                            ))
                        )}
                    </div>
                )}
            </div>
        </div>
    );
};
