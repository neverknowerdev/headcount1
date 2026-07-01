/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams, Link, useNavigate } from 'react-router-dom';
import { X, Send, Save, Archive, ExternalLink, ChevronDown, ChevronUp, RotateCcw, ArrowLeft, MoreHorizontal } from 'lucide-react';
import { useStore } from '../store';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { RunLogViewer } from './RunLogViewer';
import { useWebSocket } from '../useWebSocket';

interface TaskModalProps {
    taskId?: number | null; // If null, we are creating a new task
    projectId?: number;
    onClose: () => void;
    onTaskCreated?: () => void;
    standalone?: boolean; // render as a full page instead of an overlay modal
}

export const TaskModal: React.FC<TaskModalProps> = ({ taskId, projectId, onClose, onTaskCreated, standalone }) => {
    const navigate = useNavigate();
    const { shortName } = useParams<{shortName: string}>();
    const prefix = shortName ? shortName.toUpperCase() : 'T';
    const { selectedCompanyId } = useStore();
    const [task, setTask] = useState<any>(null);
    const [comments, setComments] = useState<any[]>([]);
    const [newComment, setNewComment] = useState('');

    const [isSaving, setIsSaving] = useState(false);
    const [runs, setRuns] = useState<any[]>([]);
    const [runAgent, setRunAgent] = useState(true);
    const [isRerunning, setIsRerunning] = useState(false);
    const [expandedComments, setExpandedComments] = useState<Set<number>>(new Set());
    const [expandedArtifact, setExpandedArtifact] = useState<number | null>(null);
    const [openRunMenu, setOpenRunMenu] = useState<number | null>(null);

    // Form data for creating or editing
    const [formData, setFormData] = useState({
        title: '',
        description: '',
        task_type: 'tech',
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

    // Fetches only comments + runs — safe to call for re-sync without resetting the form.
    const fetchActivity = useCallback(async () => {
        if (!taskId) return;
        try {
            const [commentsRes, runsRes] = await Promise.all([
                axios.get(`/api/comments?task_id=${taskId}`),
                axios.get(`/api/tasks/${taskId}/runs`),
            ]);
            setComments(commentsRes.data || []);
            setRuns(runsRes.data || []);
        } catch (e) {
            console.error(e);
        }
    }, [taskId]);

    useEffect(() => {
        if (!taskId) return;
        const load = async () => {
            try {
                const taskRes = await axios.get(`/api/tasks/${taskId}`);
                const t = taskRes.data;
                setTask(t);
                setFormData({
                    title: t.title,
                    description: t.description || t.user_input || '',
                    task_type: t.task_type || 'tech',
                    project_id: t.project_id ? t.project_id.toString() : '',
                    sprint_id: t.sprint_id ? t.sprint_id.toString() : '',
                    agent_id: t.agent_id ? t.agent_id.toString() : '',
                    priority: t.priority,
                    due_date: t.due_date ? t.due_date.split('T')[0] : '',
                    parent_id: t.parent_id ? t.parent_id.toString() : '',
                    status: t.status,
                    is_archived: t.is_archived,
                });
                await fetchActivity();
            } catch (e) {
                console.error(e);
            }
        };
        load();
    // fetchActivity is stable (useCallback on taskId), so this effectively depends only on taskId
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [taskId]);

    useWebSocket(`ws://${window.location.host}/api/ws`, (msg) => {
        if (msg.type === 'comment_created' && msg.payload.task_id === taskId) {
            setComments(prev => {
                // deduplicate: ignore if we already have this comment (e.g. from a recent re-fetch)
                if (prev.some((c: any) => c.id === msg.payload.id)) return prev;
                return [...prev, msg.payload];
            });
        }
        if (msg.type === 'task_updated' && (msg.payload.id === taskId || msg.payload.task_id === taskId)) {
            setTask((prev: any) => prev ? { ...prev, ...msg.payload } : prev);
        }
        if (msg.type === 'run_started' && msg.payload.task_id === taskId) {
            setRuns(prev => prev.some((r: any) => r.id === msg.payload.id) ? prev : [...prev, msg.payload]);
            setTask((prev: any) => prev ? { ...prev, run_id: msg.payload.id } : prev);
        }
        if (msg.type === 'run_ended') {
            const runIsOurs = runs.some((r: any) => r.id === msg.payload.run_id) || task?.run_id === msg.payload.run_id;
            setRuns(prev => {
                if (!prev.some((r: any) => r.id === msg.payload.run_id)) return prev;
                return prev.map((r: any) => r.id === msg.payload.run_id ? { ...r, status: msg.payload.status } : r);
            });
            setTask((prev: any) => prev ? { ...prev, run_id: null } : prev);
            // Re-sync after run completes to catch any comments/artifacts whose WS events may have been missed
            if (runIsOurs) fetchActivity();
        }
        if (msg.type === 'run_log') {
            if (msg.payload.entry) {
                setRuns(prev => prev.map((r: any) => r.id === msg.payload.run_id ? { ...r, log_entries: [...(r.log_entries || []), msg.payload.entry] } : r));
            } else if (msg.payload.line) {
                setRuns(prev => prev.map((r: any) => r.id === msg.payload.run_id ? { ...r, log_content: (r.log_content || '') + msg.payload.line + '\n' } : r));
            }
        }
    }, !!taskId);

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
                user_input: formData.description,
                task_type: formData.task_type,
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

    const handleRerun = async () => {
        if (!taskId) return;
        setIsRerunning(true);
        try {
            await axios.post(`/api/tasks/${taskId}/rerun`);
        } catch (e) {
            console.error(e);
            alert('Failed to start re-run');
        } finally {
            setIsRerunning(false);
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

    const header = (
        <div className="px-6 py-4 border-b flex items-center gap-4 bg-white shrink-0">
            {standalone ? (
                <button onClick={() => navigate(-1)} className="text-gray-400 hover:text-gray-700 shrink-0">
                    <ArrowLeft size={20} />
                </button>
            ) : (
                <button onClick={onClose} className="text-gray-400 hover:text-gray-600 shrink-0"><X /></button>
            )}
            {taskId ? (
                <div className="flex flex-col min-w-0">
                    <span className="text-xs font-mono text-gray-400">{prefix}-{task.id}{formData.is_archived ? <span className="ml-2 bg-red-100 text-red-800 px-1.5 py-0.5 rounded">Archived</span> : null}</span>
                    <h1 className="text-xl font-bold text-gray-900 truncate">{formData.title || task.title}</h1>
                </div>
            ) : (
                <h2 className="text-xl font-bold text-gray-900">Create New Task</h2>
            )}
        </div>
    );

    const footer = !taskId ? (
        <div className="p-4 border-t bg-white flex justify-end items-center shrink-0">
            <button type="submit" form="task-form" disabled={isSaving} className="bg-indigo-600 text-white px-4 py-2 rounded-md hover:bg-indigo-700 flex items-center">
                <Save size={16} className="mr-2" /> Create Task
            </button>
        </div>
    ) : null;

    const contentArea = (
        <div className="flex-1 overflow-y-auto flex min-h-0 overflow-x-hidden">
                <div className="flex-1 overflow-y-auto flex min-w-0">
                    {/* Left content area */}
                    <div className="flex-1 p-6 bg-gray-50 flex flex-col min-w-0">
                        <form id="task-form" onSubmit={handleSaveTask} className="space-y-4">
                            <div className="flex gap-4">
                                <div className="flex-1">
                                    <label className="block text-sm font-medium text-gray-700 mb-1">Title</label>
                                    <input required type="text" value={formData.title} onChange={e => setFormData({...formData, title: e.target.value})} className="w-full border rounded p-2 shadow-sm" placeholder="Task title" />
                                </div>
                                <div className="w-36">
                                    <label className="block text-sm font-medium text-gray-700 mb-1">Task Type</label>
                                    <select value={formData.task_type} onChange={e => setFormData({...formData, task_type: e.target.value})} className="w-full border rounded p-2 shadow-sm bg-white">
                                        <option value="tech">Tech</option>
                                        <option value="writing">Writing</option>
                                        <option value="design">Design</option>
                                    </select>
                                </div>
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
                                <p className="text-xs text-gray-500 mb-1">What you want done — this is what the AI agent uses as its input</p>
                                <textarea rows={5} value={formData.description} onChange={e => setFormData({...formData, description: e.target.value})} className="w-full border rounded p-2 shadow-sm" placeholder="Describe the task, requirements, or context for the AI agent..." />
                            </div>
                        </form>

                        {taskId && (
                            <div className="flex-1 mt-8">
                                <h3 className="text-sm font-semibold text-gray-700 mb-4 border-b pb-2">Activity</h3>
                                <div className="space-y-4 min-w-0 overflow-x-hidden" data-testid="comments-list">
                                    {(() => {
                                        // Merge comments (includes artifact_created and status_change) and runs into a single chronological timeline
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
                                                const isStatusChange = c.comment_type === 'status_change';
                                                const isArtifact = c.comment_type === 'artifact_created';
                                                const isTaskDone = c.comment_type === 'task_done';
                                                const isAskUser = c.comment_type === 'ask_user';
                                                const isAgent = c.author_type === 'agent';

                                                // Artifact entry — compact card with expandable content
                                                if (isArtifact) {
                                                    let meta: {filename?: string; content?: string} = {};
                                                    try { meta = JSON.parse(c.content); } catch {}
                                                    const isExpanded = expandedArtifact === c.id;
                                                    return (
                                                        <div key={`c-${c.id}`} className="border border-emerald-200 rounded-lg bg-emerald-50 shadow-sm overflow-hidden">
                                                            <button
                                                                className="w-full px-3 py-2 flex items-center justify-between text-xs text-left hover:bg-emerald-100 transition-colors"
                                                                onClick={() => setExpandedArtifact(isExpanded ? null : c.id)}
                                                            >
                                                                <span className="flex items-center gap-2">
                                                                    <span className="text-emerald-600">📄</span>
                                                                    <span className="font-semibold text-emerald-800">{meta.filename || 'artifact'}</span>
                                                                </span>
                                                                <div className="flex items-center gap-2 text-gray-400">
                                                                    <span>{timeStr}</span>
                                                                    {isExpanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
                                                                </div>
                                                            </button>
                                                            {isExpanded && meta.content && (
                                                                <div className="px-3 pb-3 border-t border-emerald-200 bg-white">
                                                                    <div className="prose prose-sm max-w-none mt-2 prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0 prose-pre:bg-gray-800 prose-pre:text-green-300 prose-pre:text-xs prose-code:text-emerald-700 prose-code:bg-emerald-50 prose-code:px-1 prose-code:rounded">
                                                                        <ReactMarkdown remarkPlugins={[remarkGfm]}>{meta.content}</ReactMarkdown>
                                                                    </div>
                                                                </div>
                                                            )}
                                                        </div>
                                                    );
                                                }

                                                // Compact status-change row (no bubble)
                                                if (isStatusChange) {
                                                    let meta: {from?: string; to?: string} = {};
                                                    try { meta = JSON.parse(c.content); } catch {}
                                                    const statusLabel: Record<string, string> = {
                                                        'to-do': 'To Do', 'in-progress': 'In Progress',
                                                        'in-review': 'In Review', 'done': 'Done',
                                                        'blocked': 'Blocked', 'refinement': 'Refinement',
                                                    };
                                                    const fromLabel = statusLabel[meta.from || ''] || meta.from || '';
                                                    const toLabel = statusLabel[meta.to || ''] || meta.to || '';
                                                    const actor = c.author_type === 'human' ? '👤' : '⚙️';
                                                    return (
                                                        <div key={`c-${c.id}`} className="flex items-center justify-center gap-2 text-xs text-gray-400 py-1">
                                                            <span>{actor}</span>
                                                            <span className="px-1.5 py-0.5 rounded bg-gray-100 text-gray-500 font-medium">{fromLabel}</span>
                                                            <span>→</span>
                                                            <span className="px-1.5 py-0.5 rounded bg-indigo-50 text-indigo-600 font-medium">{toLabel}</span>
                                                            <span className="text-gray-300">{timeStr}</span>
                                                        </div>
                                                    );
                                                }

                                                // Parse task_done content (JSON with msg/from/to, or legacy plain string)
                                                let taskDoneMeta: {msg?: string; from?: string; to?: string} | null = null;
                                                if (isTaskDone) {
                                                    try { taskDoneMeta = JSON.parse(c.content); } catch {}
                                                }
                                                const displayContent = taskDoneMeta?.msg ?? c.content;

                                                // Look up the run explanation for task_done comments
                                                const linkedRun = isTaskDone && c.run_id
                                                    ? runs.find((r: any) => r.id === c.run_id)
                                                    : null;
                                                const hasExplanation = linkedRun && linkedRun.result_explanation;
                                                const isExpanded = expandedComments.has(c.id);

                                                let bubbleClass = 'bg-indigo-50 border border-indigo-100 text-gray-800';
                                                if (isTaskDone) bubbleClass = 'bg-green-50 border border-green-200 text-gray-800';
                                                else if (isAskUser) bubbleClass = 'bg-amber-50 border border-amber-200 text-gray-800';
                                                else if (!isAgent) bubbleClass = 'bg-gray-200 text-gray-900';

                                                const authorLabel = isAgent ? '🤖 Agent' : '👤 You';

                                                const statusLabel: Record<string, string> = {
                                                    'to-do': 'To Do', 'in-progress': 'In Progress',
                                                    'in-review': 'In Review', 'done': 'Done',
                                                    'blocked': 'Blocked', 'refinement': 'Refinement',
                                                };

                                                return (
                                                    <div key={`c-${c.id}`} className={`flex flex-col ${isAgent ? 'items-start' : 'items-end'}`}>
                                                        <div className={`max-w-[85%] rounded-lg p-3 text-sm ${bubbleClass}`}>
                                                            <span className="text-xs font-bold flex items-center gap-2 mb-1 text-gray-500">
                                                                <span>{authorLabel}</span>
                                                                {taskDoneMeta?.from && taskDoneMeta?.to && (
                                                                    <span className="inline-flex items-center gap-1 font-normal">
                                                                        <span className="px-1.5 py-0.5 rounded bg-white/60 text-gray-500 border border-gray-200 text-xs">
                                                                            {statusLabel[taskDoneMeta.from] || taskDoneMeta.from}
                                                                        </span>
                                                                        <span className="text-gray-400">→</span>
                                                                        <span className="px-1.5 py-0.5 rounded bg-green-100 text-green-700 border border-green-200 text-xs font-medium">
                                                                            {statusLabel[taskDoneMeta.to] || taskDoneMeta.to}
                                                                        </span>
                                                                    </span>
                                                                )}
                                                                <span className="font-normal ml-auto">{timeStr}</span>
                                                            </span>
                                                            {isAgent ? (
                                                                <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0 prose-pre:bg-gray-800 prose-pre:text-green-300 prose-pre:text-xs prose-code:text-indigo-700 prose-code:bg-indigo-100 prose-code:px-1 prose-code:rounded prose-table:text-xs prose-th:px-2 prose-th:py-1 prose-td:px-2 prose-td:py-1">
                                                                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{displayContent}</ReactMarkdown>
                                                                </div>
                                                            ) : (
                                                                <span className="whitespace-pre-wrap">{displayContent}</span>
                                                            )}
                                                            {hasExplanation && (
                                                                <div className="mt-2 pt-2 border-t border-green-200">
                                                                    <button
                                                                        onClick={() => setExpandedComments(prev => {
                                                                            const next = new Set(prev);
                                                                            if (next.has(c.id)) next.delete(c.id); else next.add(c.id);
                                                                            return next;
                                                                        })}
                                                                        className="flex items-center gap-1 text-xs text-green-700 hover:text-green-900 font-medium"
                                                                    >
                                                                        {isExpanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
                                                                        {isExpanded ? 'Hide details' : 'Show details'}
                                                                    </button>
                                                                    {isExpanded && (
                                                                        <div className="mt-2 prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-pre:bg-gray-800 prose-pre:text-green-300 prose-pre:text-xs prose-code:text-indigo-700 prose-code:bg-indigo-100 prose-code:px-1 prose-code:rounded">
                                                                            <ReactMarkdown remarkPlugins={[remarkGfm]}>{linkedRun.result_explanation}</ReactMarkdown>
                                                                        </div>
                                                                    )}
                                                                </div>
                                                            )}
                                                        </div>
                                                    </div>
                                                );
                                            } else {
                                                const r = item.data;
                                                const startTs = new Date(r.started_at);
                                                const endTs = r.ended_at ? new Date(r.ended_at) : null;
                                                const displayTs = startTs.getFullYear() > 1 ? startTs : (endTs || new Date());
                                                const startStr = displayTs.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
                                                const durationMs = endTs && startTs.getFullYear() > 1 ? endTs.getTime() - startTs.getTime() : null;
                                                const durationStr = durationMs !== null
                                                    ? durationMs < 60000
                                                        ? `${Math.round(durationMs / 1000)}s`
                                                        : `${Math.floor(durationMs / 60000)}m ${Math.round((durationMs % 60000) / 1000)}s`
                                                    : null;
                                                let totalTokens = 0;
                                                if (r.token_stats) {
                                                    try { totalTokens = JSON.parse(r.token_stats).total_tokens || 0; } catch {}
                                                }
                                                const tokenStr = totalTokens >= 1000
                                                    ? `${Math.round(totalTokens / 1000)}K tok`
                                                    : totalTokens > 0 ? `${totalTokens} tok` : null;
                                                const statusColors: Record<string, string> = {
                                                    running: 'bg-yellow-100 text-yellow-800 border-yellow-200',
                                                    completed: 'bg-green-100 text-green-800 border-green-200',
                                                    failed: 'bg-red-100 text-red-800 border-red-200',
                                                    canceled: 'bg-orange-100 text-orange-800 border-orange-200',
                                                };
                                                const statusClass = statusColors[r.status] || 'bg-gray-100 text-gray-800 border-gray-200';
                                                const maxRunId = Math.max(...runs.map((x: any) => x.id));
                                                const isLatest = r.id === maxRunId;
                                                const menuOpen = openRunMenu === r.id;
                                                return (
                                                    <div key={`r-${r.id}`} className="w-full min-w-0">
                                                        <details className="w-full min-w-0 border rounded-lg bg-white shadow-sm">
                                                            <summary className="px-3 py-2 cursor-pointer flex items-center justify-between text-xs">
                                                                <span className="font-semibold text-gray-600">⚙️ Run #{r.id}</span>
                                                                <div className="flex items-center gap-2">
                                                                    <span className={`px-2 py-0.5 rounded-full border text-xs font-medium ${statusClass}`}>{r.status}</span>
                                                                    <span className="text-gray-400">{startStr}</span>
                                                                    {durationStr && <span className="text-gray-400">{durationStr}</span>}
                                                                    {tokenStr && <span className="text-gray-400">{tokenStr}</span>}
                                                                    <div className="relative">
                                                                        {menuOpen && <div className="fixed inset-0 z-10" onClick={e => { e.stopPropagation(); setOpenRunMenu(null); }} />}
                                                                        <button
                                                                            onClick={e => { e.preventDefault(); e.stopPropagation(); setOpenRunMenu(menuOpen ? null : r.id); }}
                                                                            className={`relative z-20 p-1 rounded hover:bg-gray-100 ${menuOpen ? 'text-gray-700 bg-gray-100' : 'text-gray-400 hover:text-gray-600'}`}
                                                                        >
                                                                            <MoreHorizontal size={15} />
                                                                        </button>
                                                                        {menuOpen && (
                                                                            <div className="absolute right-0 top-6 z-20 bg-white border rounded-lg shadow-lg py-1 min-w-[130px]">
                                                                                {isLatest && r.status !== 'running' && (
                                                                                    <button
                                                                                        onClick={e => { e.preventDefault(); setOpenRunMenu(null); handleRerun(); }}
                                                                                        disabled={isRerunning}
                                                                                        className="w-full text-left px-3 py-1.5 text-xs hover:bg-gray-50 flex items-center gap-2 disabled:opacity-50"
                                                                                    >
                                                                                        <RotateCcw size={11} /> Re-run
                                                                                    </button>
                                                                                )}
                                                                                <Link
                                                                                    to={`/companies/${shortName}/run-logs/${r.id}`}
                                                                                    onClick={() => setOpenRunMenu(null)}
                                                                                    className="block px-3 py-1.5 text-xs hover:bg-gray-50 flex items-center gap-2 text-gray-700"
                                                                                >
                                                                                    <ExternalLink size={11} /> View full log
                                                                                </Link>
                                                                            </div>
                                                                        )}
                                                                    </div>
                                                                </div>
                                                            </summary>
                                                            <div className="border-t rounded-b-lg overflow-hidden">
                                                                <RunLogViewer
                                                                    compact
                                                                    messages={(r.log_entries || []).map((e: any, i: number) => ({ id: i, entry: e }))}
                                                                    status={r.status}
                                                                />
                                                            </div>
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
                    <div className="w-72 shrink-0 bg-white border-l flex flex-col">
                        <div className="flex-1 p-6 space-y-4 overflow-y-auto">
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
                        {taskId && (
                            <div className="p-4 border-t space-y-2 shrink-0">
                                <button type="submit" form="task-form" disabled={isSaving} className="w-full bg-indigo-600 text-white px-4 py-2 rounded-md hover:bg-indigo-700 flex items-center justify-center text-sm">
                                    <Save size={16} className="mr-2" /> Save Task
                                </button>
                                <button type="button" onClick={handleArchive} className="w-full text-red-600 hover:text-red-800 text-sm flex items-center justify-center px-4 py-2 border border-red-200 rounded hover:bg-red-50">
                                    <Archive size={16} className="mr-2" /> {formData.is_archived ? "Unarchive" : "Archive"}
                                </button>
                            </div>
                        )}
                    </div>
                </div>
        </div>
    );

    if (standalone) {
        return (
            <div className="h-full flex flex-col">
                {header}
                <div className="flex-1 min-h-0">{contentArea}</div>
                {footer}
            </div>
        );
    }

    return (
        <div className="fixed inset-0 bg-black/50 bg-opacity-50 flex justify-center items-center z-50">
            <div className="bg-white rounded-lg shadow-xl w-full max-w-4xl max-h-[90vh] flex flex-col">
                {header}
                <div className="flex-1 min-h-0">{contentArea}</div>
                {footer}
            </div>
        </div>
    );
};

export default TaskModal;
