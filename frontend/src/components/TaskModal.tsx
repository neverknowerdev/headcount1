/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { X, Send, Save, Archive, ExternalLink } from 'lucide-react';
import { useStore } from '../store';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

interface TaskModalProps {
    taskId?: number | null; // If null, we are creating a new task
    projectId?: number;
    onClose: () => void;
    onTaskCreated?: () => void;
}

export const TaskModal: React.FC<TaskModalProps> = ({ taskId, projectId, onClose, onTaskCreated }) => {
    const { shortName } = useParams<{shortName: string}>();
    const prefix = shortName ? shortName.toUpperCase() : 'T';
    const { selectedCompanyId } = useStore();
    const [task, setTask] = useState<any>(null);
    const [comments, setComments] = useState<any[]>([]);
    const [newComment, setNewComment] = useState('');

    const [isSaving, setIsSaving] = useState(false);
    const [runs, setRuns] = useState<any[]>([]);
    const [runAgent, setRunAgent] = useState(true);

    // Form data for creating or editing
    const [formData, setFormData] = useState({
        title: '',
        description: '',
        project_id: projectId?.toString() || '',
        sprint_id: '',
        agent_id: '',
        priority: 'Normal',
        due_date: '',
        parent_id: '',
        status: 'backlog',
        is_archived: false,
    });

    // Metadata
    const [projects, setProjects] = useState<any[]>([]);
    const [sprints, setSprints] = useState<any[]>([]);
    const [agents, setAgents] = useState<any[]>([]);
    const [allTasks, setAllTasks] = useState<any[]>([]);
    const [parentSearch, setParentSearch] = useState('');
    const [showParentDropdown, setShowParentDropdown] = useState(false);

    useEffect(() => {
        if (!selectedCompanyId) return;
        Promise.all([
            axios.get(`/api/projects?company_id=${selectedCompanyId}`),
            axios.get(`/api/sprints?company_id=${selectedCompanyId}`),
            axios.get(`/api/agents?company_id=${selectedCompanyId}`),
            axios.get(`/api/tasks?company_id=${selectedCompanyId}`)
        ]).then(([projRes, sprintRes, agentRes, tasksRes]) => {
            setAllTasks(tasksRes.data || []);
            setProjects(projRes.data || []);
            const fetchedSprints = sprintRes.data || [];
            setSprints(fetchedSprints);
            setAgents(agentRes.data || []);

            // Auto-select first sprint if creating and no sprint is set
            let updates: any = {};
            if (!taskId && !formData.sprint_id && fetchedSprints.length > 0) {
                updates.sprint_id = fetchedSprints[0].id.toString();
            }
            if (!taskId && !formData.agent_id && agentRes.data?.length > 0) {
                const ceo = agentRes.data.find((a: any) => a.name.toLowerCase().includes('ceo'));
                updates.agent_id = ceo ? ceo.id.toString() : agentRes.data[0].id.toString();
            }
            if (Object.keys(updates).length > 0) {
                setFormData(prev => ({...prev, ...updates}));
            }
        });
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [selectedCompanyId, taskId]);

    useEffect(() => {
        if (!taskId) return;
        const fetchDetails = async () => {
            try {
                const [taskRes, commentsRes, runsRes] = await Promise.all([
                    axios.get(`/api/tasks/${taskId}`),
                    axios.get(`/api/comments?task_id=${taskId}`),
                    axios.get(`/api/tasks/${taskId}/runs`)
                ]);
                const t = taskRes.data;
                setTask(t);
                setComments(commentsRes.data || []);
                setRuns(runsRes.data || []);
                setFormData({
                    title: t.title,
                    description: t.description || '',
                    project_id: t.project_id ? t.project_id.toString() : '',
                    sprint_id: t.sprint_id ? t.sprint_id.toString() : '',
                    agent_id: t.agent_id ? t.agent_id.toString() : '',
                    priority: t.priority,
                    due_date: t.due_date ? t.due_date.split('T')[0] : '',
                    parent_id: t.parent_id ? t.parent_id.toString() : '',
                    status: t.status,
                    is_archived: t.is_archived,
                });
            } catch (e) {
                console.error(e);
            }
        };
        fetchDetails();

        const ws = new WebSocket(`ws://${window.location.host}/api/ws`);
        ws.onmessage = (event) => {
            const msg = JSON.parse(event.data);
            if (msg.type === 'comment_created' && msg.payload.task_id === taskId) {
                setComments(prev => [...prev, msg.payload]);
            }
            if (msg.type === 'run_started' && msg.payload.task_id === taskId) {
                setRuns(prev => [...prev, msg.payload]);
                setTask((prev: any) => prev ? { ...prev, run_id: msg.payload.id } : prev);
            }
            if (msg.type === 'run_ended' && runs.some((r: any) => r.id === msg.payload.run_id)) {
                setRuns(prev => prev.map((r: any) => r.id === msg.payload.run_id ? { ...r, status: msg.payload.status } : r));
                setTask((prev: any) => prev ? { ...prev, run_id: null } : prev);
            }
            if (msg.type === 'run_log') {
                setRuns(prev => prev.map((r: any) => r.id === msg.payload.run_id ? { ...r, log_content: (r.log_content || '') + msg.payload.line + '\n' } : r));
            }
        };
        return () => ws.close();
    }, [taskId]);

    const handleAddComment = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!newComment.trim() || !taskId) return;
        try {
            await axios.post('/api/comments', {
                task_id: taskId,
                author_type: 'human',
                content: newComment,
                run_agent: runAgent
            });
            setNewComment('');
        } catch (e) {
            console.error(e);
        }
    };

    const handleSaveTask = async (e: React.FormEvent) => {
        e.preventDefault();
        setIsSaving(true);
        try {
            const payload: any = {
                title: formData.title,
                description: formData.description,
                priority: formData.priority,
            };
            if (!taskId) {
                payload.company_id = selectedCompanyId;
            }
            if (formData.project_id) payload.project_id = parseInt(formData.project_id);
            if (formData.sprint_id) payload.sprint_id = parseInt(formData.sprint_id);
            if (formData.agent_id) payload.agent_id = parseInt(formData.agent_id);
            if (formData.parent_id) payload.parent_id = parseInt(formData.parent_id);
            if (formData.due_date) payload.due_date = new Date(formData.due_date).toISOString();

            if (taskId) {
                payload.status = formData.status;
                payload.is_archived = formData.is_archived;
                await axios.put(`/api/tasks/${taskId}`, payload);
            } else {
                await axios.post('/api/tasks', payload);
            }

            if (onTaskCreated) onTaskCreated();
            onClose();
        } catch (e: any) {
            console.error(e);
            alert(e.response?.data?.error || "Failed to save task. Ensure you have a Sprint created and selected.");
        } finally {
            setIsSaving(false);
        }
    };

    const handleArchive = async () => {
        if (!taskId) return;
        if (!window.confirm(formData.is_archived ? "Unarchive this task?" : "Are you sure you want to archive this task?")) return;

        try {
            await axios.put(`/api/tasks/${taskId}`, {
                is_archived: !formData.is_archived
            });
            if (onTaskCreated) onTaskCreated();
            onClose();
        } catch (e) {
            console.error(e);
        }
    };

    if (taskId && !task) return null;

    return (
        <div className="fixed inset-0 bg-black/50 bg-opacity-50 flex justify-center items-center z-50">
            <div className="bg-white rounded-lg shadow-xl w-full max-w-4xl max-h-[90vh] flex flex-col">
                <div className="p-6 border-b flex justify-between items-center">
                    <div>
                        {taskId ? (
                            <>
                                <span className="text-xs font-semibold text-gray-500 uppercase tracking-wider block mb-1">
                                    Task {prefix}-{task.id} {formData.is_archived ? <span className="ml-2 bg-red-100 text-red-800 px-2 py-0.5 rounded">Archived</span> : null}
                                </span>
                            </>
                        ) : (
                            <h2 className="text-xl font-bold text-gray-900">Create New Task</h2>
                        )}
                    </div>
                    <button onClick={onClose} className="text-gray-400 hover:text-gray-600"><X /></button>
                </div>

                <div className="flex-1 overflow-y-auto flex">
                    {/* Left content area */}
                    <div className="flex-1 p-6 bg-gray-50 flex flex-col">
                        <form id="task-form" onSubmit={handleSaveTask} className="space-y-4">
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Title</label>
                                <input required type="text" value={formData.title} onChange={e => setFormData({...formData, title: e.target.value})} className="w-full border rounded p-2 shadow-sm" placeholder="Task title" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
                                <textarea rows={5} value={formData.description} onChange={e => setFormData({...formData, description: e.target.value})} className="w-full border rounded p-2 shadow-sm" placeholder="Task details..." />
                            </div>
                        </form>

                        {taskId && (
                            <div className="flex-1 mt-8">
                                <h3 className="text-sm font-semibold text-gray-700 mb-4 border-b pb-2">Activity</h3>
                                <div className="space-y-4" data-testid="comments-list">
                                    {(() => {
                                        // Merge comments and runs into a single chronological timeline
                                        const timeline: {type: 'comment' | 'run'; data: any; time: number}[] = [];
                                        comments.forEach((c: any) => {
                                            timeline.push({ type: 'comment', data: c, time: new Date(c.created_at).getTime() });
                                        });
                                        runs.forEach((r: any) => {
                                            const t = new Date(r.started_at);
                                            const runTime = t.getFullYear() > 1 ? t.getTime() : new Date(r.ended_at || Date.now()).getTime();
                                            timeline.push({ type: 'run', data: r, time: runTime });
                                        });
                                        timeline.sort((a, b) => a.time - b.time);

                                        if (timeline.length === 0) {
                                            return <p className="text-sm text-gray-500 italic">No activity yet.</p>;
                                        }

                                        return timeline.map((item) => {
                                            if (item.type === 'comment') {
                                                const c = item.data;
                                                const ts = new Date(c.created_at);
                                                const timeStr = ts.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
                                                return (
                                                    <div key={`c-${c.id}`} className={`flex flex-col ${c.author_type === 'agent' ? 'items-start' : 'items-end'}`}>
                                                        <div className={`max-w-[85%] rounded-lg p-3 text-sm ${c.author_type === 'agent' ? 'bg-indigo-50 border border-indigo-100 text-gray-800' : 'bg-gray-200 text-gray-900'}`}>
                                                            <span className="text-xs font-bold block mb-1 text-gray-500">
                                                                {c.author_type === 'agent' ? '🤖 Agent' : '👤 You'}
                                                                <span className="ml-2 font-normal">{timeStr}</span>
                                                            </span>
                                                            {c.author_type === 'agent' ? (
                                                                <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0 prose-pre:bg-gray-800 prose-pre:text-green-300 prose-pre:text-xs prose-code:text-indigo-700 prose-code:bg-indigo-100 prose-code:px-1 prose-code:rounded prose-table:text-xs prose-th:px-2 prose-th:py-1 prose-td:px-2 prose-td:py-1">
                                                                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{c.content}</ReactMarkdown>
                                                                </div>
                                                            ) : (
                                                                <span className="whitespace-pre-wrap">{c.content}</span>
                                                            )}
                                                        </div>
                                                    </div>
                                                );
                                            } else {
                                                const r = item.data;
                                                const ts = new Date(r.started_at);
                                                const runDate = ts.getFullYear() > 1 ? ts : new Date(r.ended_at || Date.now());
                                                const timeStr = runDate.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
                                                const statusColors: Record<string, string> = {
                                                    running: 'bg-yellow-100 text-yellow-800 border-yellow-200',
                                                    completed: 'bg-green-100 text-green-800 border-green-200',
                                                    failed: 'bg-red-100 text-red-800 border-red-200',
                                                    canceled: 'bg-orange-100 text-orange-800 border-orange-200',
                                                };
                                                const statusClass = statusColors[r.status] || 'bg-gray-100 text-gray-800 border-gray-200';
                                                return (
                                                    <div key={`r-${r.id}`} className="flex justify-center">
                                                        <details className="w-full max-w-[90%] border rounded-lg bg-white shadow-sm">
                                                            <summary className="px-3 py-2 cursor-pointer flex items-center justify-between text-xs">
                                                                <span className="font-semibold text-gray-600">⚙️ Run #{r.id}</span>
                                                                <div className="flex items-center gap-2">
                                                                    <span className={`px-2 py-0.5 rounded-full border text-xs font-medium ${statusClass}`}>{r.status}</span>
                                                                    <span className="text-gray-400">{timeStr}</span>
                                                                    <Link to={`/companies/${shortName}/run-logs/${r.id}`} className="text-gray-400 hover:text-indigo-600" title="View full log">
                                                                        <ExternalLink size={14} />
                                                                    </Link>
                                                                </div>
                                                            </summary>
                                                            <pre className="text-xs bg-gray-900 text-green-400 p-3 rounded-b-lg overflow-x-auto whitespace-pre-wrap border-t">
                                                                {r.log_content}
                                                            </pre>
                                                        </details>
                                                    </div>
                                                );
                                            }
                                        });
                                    })()}
                                </div>
                                <div className="mt-4">
                                    {task?.run_id ? (
                                        <div className="group relative">
                                            <input
                                                type="text"
                                                disabled
                                                placeholder="Agent is running... Comments are disabled until it finishes"
                                                className="flex-1 border-gray-300 rounded-md shadow-sm border p-2 text-sm bg-gray-100 text-gray-500 cursor-not-allowed"
                                            />
                                            <div className="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 hidden group-hover:block bg-gray-900 text-white text-xs rounded px-3 py-1.5 whitespace-nowrap z-50">
                                                Comments are disabled while an agent run is in progress
                                                <div className="absolute top-full left-1/2 -translate-x-1/2 border-4 border-transparent border-t-gray-900"></div>
                                            </div>
                                        </div>
                                    ) : (
                                        <form onSubmit={handleAddComment} className="flex gap-2">
                                            <input
                                                type="text"
                                                value={newComment}
                                                onChange={(e) => setNewComment(e.target.value)}
                                                placeholder="Add a comment..."
                                                className="flex-1 border-gray-300 rounded-md shadow-sm border p-2 focus:ring-indigo-500 focus:border-indigo-500 text-sm"
                                            />
                                            <div className="flex items-center px-2">
                                                <input
                                                    type="checkbox"
                                                    id="runAgentCheckbox"
                                                    checked={runAgent}
                                                    onChange={(e) => setRunAgent(e.target.checked)}
                                                />
                                                <label htmlFor="runAgentCheckbox" className="ml-1 text-xs text-gray-600">Run Agent</label>
                                            </div>
                                            <button type="submit" className="bg-indigo-600 text-white p-2 rounded-md hover:bg-indigo-700">
                                                <Send size={18} />
                                            </button>
                                        </form>
                                    )}
                                </div>
                            </div>
                        )}
                    </div>

                    {/* Right sidebar */}
                    <div className="w-72 bg-white border-l p-6 space-y-4 overflow-y-auto">
                        {taskId && (
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Status</label>
                                <select value={formData.status} onChange={e => setFormData({...formData, status: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm font-semibold text-indigo-600">
                                    <option value="backlog">Backlog</option>
                                    <option value="to-do">To Do</option>
                                    <option value="refinement">Refinement</option>
                                    <option value="in-progress">In Progress</option>
                                    <option value="in-review">In Review</option>
                                    <option value="blocked">Blocked</option>
                                    <option value="done">Done</option>
                                </select>
                            </div>
                        )}
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Project</label>
                            <select value={formData.project_id} onChange={e => setFormData({...formData, project_id: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm">
                                <option value="">-- No Project --</option>
                                {projects.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                            </select>
                        </div>
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Sprint</label>
                            <select required value={formData.sprint_id} onChange={e => setFormData({...formData, sprint_id: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm">
                                <option value="" disabled>-- Select Sprint --</option>
                                {sprints.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}
                            </select>
                        </div>
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Assignee</label>
                            <select value={formData.agent_id} onChange={e => setFormData({...formData, agent_id: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm">
                                <option value="">Unassigned</option>
                                {agents.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}
                            </select>
                        </div>
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Priority</label>
                            <select value={formData.priority} onChange={e => setFormData({...formData, priority: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm">
                                <option value="Low">Low</option>
                                <option value="Normal">Normal</option>
                                <option value="High">High</option>
                                <option value="Urgent">Urgent</option>
                            </select>
                        </div>
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Due Date</label>
                            <input type="date" value={formData.due_date} onChange={e => setFormData({...formData, due_date: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm" />
                        </div>
                        <div className="relative">
                            <label className="block text-sm font-medium text-gray-700 mb-1">Parent Task</label>
                            <div className="flex space-x-2 items-center">
                                <input
                                    type="text"
                                    value={formData.parent_id ? `${prefix}-${formData.parent_id}` : parentSearch}
                                    onChange={e => {
                                        setFormData({...formData, parent_id: ''});
                                        setParentSearch(e.target.value);
                                        setShowParentDropdown(true);
                                    }}
                                    onFocus={() => setShowParentDropdown(true)}
                                    className="w-full border rounded p-2 text-sm shadow-sm"
                                    placeholder="Search by title or ID..."
                                />
                                {formData.parent_id && (
                                    <button type="button" onClick={() => { setFormData({...formData, parent_id: ''}); setParentSearch(''); }} className="text-gray-400 hover:text-red-500">
                                        <X size={16} />
                                    </button>
                                )}
                            </div>

                            {showParentDropdown && !formData.parent_id && (
                                <div className="absolute z-10 w-full mt-1 bg-white border rounded shadow-lg max-h-48 overflow-y-auto">
                                    {allTasks.filter(t =>
                                        t.id !== taskId && // Can't be parent of itself
                                        (t.title.toLowerCase().includes(parentSearch.toLowerCase()) || t.id.toString().includes(parentSearch))
                                    ).slice(0, 10).map(t => (
                                        <div
                                            key={t.id}
                                            className="p-2 text-sm hover:bg-indigo-50 cursor-pointer border-b last:border-b-0"
                                            onClick={() => {
                                                setFormData({...formData, parent_id: t.id.toString()});
                                                setParentSearch('');
                                                setShowParentDropdown(false);
                                            }}
                                        >
                                            <span className="text-xs text-gray-500 font-mono mr-2">{prefix}-{t.id}</span>
                                            {t.title}
                                        </div>
                                    ))}
                                </div>
                            )}
                        </div>
                    </div>
                </div>

                <div className="p-4 border-t bg-white flex justify-between items-center">
                    <div>
                        {taskId && (
                            <button type="button" onClick={handleArchive} className="text-red-600 hover:text-red-800 text-sm flex items-center px-4 py-2 border border-red-200 rounded hover:bg-red-50">
                                <Archive size={16} className="mr-2" /> {formData.is_archived ? "Unarchive" : "Archive"}
                            </button>
                        )}
                    </div>
                    <div className="flex space-x-3">
                        <button onClick={onClose} className="text-gray-600 hover:text-gray-900 px-4 py-2">Cancel</button>
                        <button type="submit" form="task-form" disabled={isSaving} className="bg-indigo-600 text-white px-4 py-2 rounded-md hover:bg-indigo-700 flex items-center">
                            <Save size={16} className="mr-2" /> {taskId ? "Save Task" : "Create Task"}
                        </button>
                    </div>
                </div>
            </div>
        </div>
    );
};
