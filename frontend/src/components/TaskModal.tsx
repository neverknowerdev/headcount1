/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { X, Send } from 'lucide-react';
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

    // Form data for creating
    const [formData, setFormData] = useState({
        title: '',
        description: '',
        project_id: projectId || '',
        sprint_id: '',
        agent_id: '',
        priority: 'Normal',
        due_date: '',
        parent_id: ''
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
            setSprints(sprintRes.data || []);
            setAgents(agentRes.data || []);
        });
    }, [selectedCompanyId]);

    useEffect(() => {
        if (!taskId) return;
        const fetchDetails = async () => {
            try {
                const [taskRes, commentsRes] = await Promise.all([
                    axios.get(`/api/tasks/${taskId}`),
                    axios.get(`/api/comments?task_id=${taskId}`)
                ]);
                setTask(taskRes.data);
                setComments(commentsRes.data || []);
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

    const handleCreateTask = async (e: React.FormEvent) => {
        e.preventDefault();
        try {
            const payload: any = {
                title: formData.title,
                description: formData.description,
                project_id: parseInt(formData.project_id as string),
                priority: formData.priority,
            };
            if (formData.sprint_id) payload.sprint_id = parseInt(formData.sprint_id);
            if (formData.agent_id) payload.agent_id = parseInt(formData.agent_id);
            if (formData.parent_id) payload.parent_id = parseInt(formData.parent_id);
            if (formData.due_date) payload.due_date = new Date(formData.due_date).toISOString();

            await axios.post('/api/tasks', payload);
            if (onTaskCreated) onTaskCreated();
            onClose();
        } catch (e) {
            console.error(e);
            alert("Failed to create task");
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
                                <span className="text-xs font-semibold text-gray-500 uppercase tracking-wider block mb-1">Task T-{task.id}</span>
                                <h2 className="text-xl font-bold text-gray-900">{task.title}</h2>
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
                        {taskId ? (
                            <div className="space-y-6">
                                {task.description && (
                                    <div>
                                        <h3 className="text-sm font-semibold text-gray-700 mb-2">Description</h3>
                                        <p className="text-gray-600 text-sm whitespace-pre-wrap">{task.description}</p>
                                    </div>
                                )}
                                <div className="flex-1">
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
                                </div>
                            </div>
                        ) : (
                            <form id="create-task-form" onSubmit={handleCreateTask} className="space-y-4">
                                <div>
                                    <label className="block text-sm font-medium text-gray-700 mb-1">Title</label>
                                    <input required type="text" value={formData.title} onChange={e => setFormData({...formData, title: e.target.value})} className="w-full border rounded p-2 shadow-sm" placeholder="Task title" />
                                </div>
                                <div>
                                    <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
                                    <textarea rows={5} value={formData.description} onChange={e => setFormData({...formData, description: e.target.value})} className="w-full border rounded p-2 shadow-sm" placeholder="Task details..." />
                                </div>
                            </form>
                        )}
                    </div>

                    {/* Right sidebar */}
                    <div className="w-72 bg-white border-l p-6 space-y-4 overflow-y-auto">
                        {taskId ? (
                            <>
                                <div>
                                    <h4 className="text-xs font-bold text-gray-500 uppercase tracking-wider mb-1">Status</h4>
                                    <span className="text-sm bg-gray-100 px-2 py-1 rounded">{task.status}</span>
                                </div>
                                <div>
                                    <h4 className="text-xs font-bold text-gray-500 uppercase tracking-wider mb-1">Priority</h4>
                                    <span className="text-sm">{task.priority}</span>
                                </div>
                                {task.sprint_id && (
                                    <div>
                                        <h4 className="text-xs font-bold text-gray-500 uppercase tracking-wider mb-1">Sprint ID</h4>
                                        <span className="text-sm">{task.sprint_id}</span>
                                    </div>
                                )}
                                {task.due_date && (
                                    <div>
                                        <h4 className="text-xs font-bold text-gray-500 uppercase tracking-wider mb-1">Due Date</h4>
                                        <span className="text-sm">{new Date(task.due_date).toLocaleDateString()}</span>
                                    </div>
                                )}
                            </>
                        ) : (
                            <>
                                <div>
                                    <label className="block text-sm font-medium text-gray-700 mb-1">Project</label>
                                    <select required value={formData.project_id} onChange={e => setFormData({...formData, project_id: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm">
                                        <option value="">-- Select Project --</option>
                                        {projects.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                                    </select>
                                </div>
                                <div>
                                    <label className="block text-sm font-medium text-gray-700 mb-1">Sprint</label>
                                    <select value={formData.sprint_id} onChange={e => setFormData({...formData, sprint_id: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm">
                                        <option value="">Current Sprint (Default)</option>
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
                            </>
                        )}
                    </div>
                </div>

                <div className="p-4 border-t bg-white">
                    {taskId ? (
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
                    ) : (
                        <div className="flex justify-end space-x-3">
                            <button onClick={onClose} className="text-gray-600 hover:text-gray-900 px-4 py-2">Cancel</button>
                            <button type="submit" form="create-task-form" className="bg-indigo-600 text-white px-4 py-2 rounded-md hover:bg-indigo-700">Create Task</button>
                        </div>
                    )}
                </div>
            </div>
        </div>
    );
};
