/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { X, Send, Save, Archive } from 'lucide-react';
import { useStore } from '../store';

interface TaskModalProps {
    taskId?: number | null; // If null, we are creating a new task
    projectId?: number;
    onClose: () => void;
    onTaskCreated?: () => void;
}

export const TaskModal: React.FC<TaskModalProps> = ({ taskId, projectId, onClose, onTaskCreated }) => {
    const { selectedCompanyId } = useStore();
    const [task, setTask] = useState<any>(null);
    const [comments, setComments] = useState<any[]>([]);
    const [newComment, setNewComment] = useState('');

    const [isSaving, setIsSaving] = useState(false);

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

    useEffect(() => {
        if (!selectedCompanyId) return;
        Promise.all([
            axios.get(`/api/projects?company_id=${selectedCompanyId}`),
            axios.get(`/api/sprints?company_id=${selectedCompanyId}`),
            axios.get(`/api/agents?company_id=${selectedCompanyId}`)
        ]).then(([projRes, sprintRes, agentRes]) => {
            setProjects(projRes.data || []);
            const fetchedSprints = sprintRes.data || [];
            setSprints(fetchedSprints);
            setAgents(agentRes.data || []);

            // Auto-select first sprint if creating and no sprint is set
            if (!taskId && !formData.sprint_id && fetchedSprints.length > 0) {
                setFormData(prev => ({...prev, sprint_id: fetchedSprints[0].id.toString()}));
            }
        });
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [selectedCompanyId, taskId]);

    useEffect(() => {
        if (!taskId) return;
        const fetchDetails = async () => {
            try {
                const [taskRes, commentsRes] = await Promise.all([
                    axios.get(`/api/tasks/${taskId}`),
                    axios.get(`/api/comments?task_id=${taskId}`)
                ]);
                const t = taskRes.data;
                setTask(t);
                setComments(commentsRes.data || []);
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
                content: newComment
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
                                    Task T-{task.id} {formData.is_archived ? <span className="ml-2 bg-red-100 text-red-800 px-2 py-0.5 rounded">Archived</span> : null}
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
                                <h3 className="text-sm font-semibold text-gray-700 mb-4 border-b pb-2">Comments & Activity</h3>
                                <div className="space-y-4" data-testid="comments-list">
                                    {comments.length === 0 ? (
                                        <p className="text-sm text-gray-500 italic">No comments yet.</p>
                                    ) : (
                                        comments.map((c: any) => (
                                            <div key={c.id} className={`flex flex-col ${c.author_type === 'agent' ? 'items-start' : 'items-end'}`}>
                                                <div className={`max-w-[80%] rounded-lg p-3 text-sm ${c.author_type === 'agent' ? 'bg-indigo-50 border border-indigo-100 text-gray-800' : 'bg-gray-200 text-gray-900'}`}>
                                                    <span className="text-xs font-bold block mb-1 text-gray-500">
                                                        {c.author_type === 'agent' ? '🤖 Agent' : '👤 You'}
                                                    </span>
                                                    <span className="whitespace-pre-wrap">{c.content}</span>
                                                </div>
                                            </div>
                                        ))
                                    )}
                                </div>
                                <div className="mt-4">
                                    <form onSubmit={handleAddComment} className="flex gap-2">
                                        <input
                                            type="text"
                                            value={newComment}
                                            onChange={(e) => setNewComment(e.target.value)}
                                            placeholder="Add a comment..."
                                            className="flex-1 border-gray-300 rounded-md shadow-sm border p-2 focus:ring-indigo-500 focus:border-indigo-500 text-sm"
                                        />
                                        <button type="submit" className="bg-indigo-600 text-white p-2 rounded-md hover:bg-indigo-700">
                                            <Send size={18} />
                                        </button>
                                    </form>
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
                        <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">Parent Task ID</label>
                            <input type="number" value={formData.parent_id} onChange={e => setFormData({...formData, parent_id: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm" placeholder="e.g. 12" />
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
