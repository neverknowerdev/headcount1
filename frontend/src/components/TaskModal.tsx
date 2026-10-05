/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams, Link, useNavigate } from 'react-router-dom';
import { X, Send, Save, Archive, ExternalLink, ChevronDown, ChevronUp, RotateCcw, ArrowLeft, MoreHorizontal } from 'lucide-react';
import { useStore } from '../store';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { getActivityAuthorLabel } from '../utils/activityDisplay';
import { RunLogViewer } from './RunLogViewer';
import { getRunAgentName } from '../utils/runDisplay';
import { TaskRelations } from './TaskRelations';
import { useWebSocket, wsUrl } from '../useWebSocket';

// parseSpecItems decodes a structured acceptance-criteria / test-cases item
// list. Returns null for legacy plain-text content (rendered as markdown).
function parseSpecItems(raw: string): any[] | null {
    try {
        const v = JSON.parse(raw);
        return Array.isArray(v) ? v : null;
    } catch {
        return null;
    }
}

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
    const [commentError, setCommentError] = useState('');
    const [isPostingComment, setIsPostingComment] = useState(false);

    const [isSaving, setIsSaving] = useState(false);
    const [runs, setRuns] = useState<any[]>([]);
    const [runAgent, setRunAgent] = useState(true);
    const [isRerunning, setIsRerunning] = useState(false);
    const [expandedComments, setExpandedComments] = useState<Set<number>>(new Set());
    const [expandedArtifact, setExpandedArtifact] = useState<number | null>(null);
    const [openRunMenu, setOpenRunMenu] = useState<number | null>(null);
    // Acceptance Criteria / Test Cases sections are minimized by default.
    const [expandedSpecs, setExpandedSpecs] = useState<Set<string>>(new Set());
    // Artifacts block (task-level deliverables) — minimized by default.
    const [artifacts, setArtifacts] = useState<any[]>([]);
    const [artifactsExpanded, setArtifactsExpanded] = useState(false);
    const [expandedArtifactIds, setExpandedArtifactIds] = useState<Set<number>>(new Set());
	// Git branch selection is an advanced, opt-in task setting. Keep it
	// collapsed by default so task creation stays focused on the work itself.
	const [gitOptionsExpanded, setGitOptionsExpanded] = useState(false);

    // Form data for creating or editing
    const [formData, setFormData] = useState({
        title: '',
        description: '',
        project_id: projectId?.toString() || '',
        sprint_id: '',
        agent_id: '',
        priority: 'Normal',
        git_base_branch: 'main',
        due_date: '',
        parent_id: '',
        status: 'backlog',
        is_archived: false,
    });

    // Metadata
    const [projects, setProjects] = useState<any[]>([]);
	const [projectBranches, setProjectBranches] = useState<string[]>(['main']);
	const [branchesLoading, setBranchesLoading] = useState(false);
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

    // Fetches only comments + runs + artifacts — safe to call for re-sync
    // without resetting the form.
    const fetchActivity = useCallback(async () => {
        if (!taskId) return;
        try {
            const [commentsRes, runsRes, artifactsRes] = await Promise.all([
                axios.get(`/api/comments?task_id=${taskId}`),
                axios.get(`/api/tasks/${taskId}/runs`),
                axios.get(`/api/tasks/${taskId}/artifacts`),
            ]);
            setComments(commentsRes.data || []);
            setRuns(runsRes.data || []);
            setArtifacts(artifactsRes.data || []);
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
                    description: t.description || '',
                    project_id: t.project_id ? t.project_id.toString() : '',
                    sprint_id: t.sprint_id ? t.sprint_id.toString() : '',
                    agent_id: t.agent_id ? t.agent_id.toString() : '',
                    priority: t.priority,
					git_base_branch: t.git_base_branch || 'main',
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

	// Branches are project-specific. Fetch them only after the project is known,
	// preserving the task's saved branch even if it was deleted remotely later.
	useEffect(() => {
		if (!formData.project_id) {
			setProjectBranches(['main']);
			return;
		}
		let cancelled = false;
		setBranchesLoading(true);
		axios.get(`/api/projects/${formData.project_id}/branches`)
			.then(res => {
				if (cancelled) return;
				const branches = Array.isArray(res.data) ? res.data : [];
				// Preserve the server's newest-first order. A deleted saved branch is
				// kept at the end so the task remains readable/editable.
				setProjectBranches(Array.from(new Set([...branches, formData.git_base_branch, 'main'])).filter(Boolean));
			})
			.catch(() => {
				if (!cancelled) setProjectBranches(Array.from(new Set([formData.git_base_branch, 'main'])).filter(Boolean));
			})
			.finally(() => { if (!cancelled) setBranchesLoading(false); });
		return () => { cancelled = true; };
	}, [formData.project_id]);

    // Re-sync after a (re)connect: recover comments/runs/status changes whose
    // WS events were missed while disconnected. Deliberately does not touch
    // formData so in-progress edits are never clobbered.
    const resyncAfterReconnect = useCallback(async () => {
        if (!taskId) return;
        fetchActivity();
        try {
            const res = await axios.get(`/api/tasks/${taskId}`);
            setTask((prev: any) => prev ? { ...prev, ...res.data } : res.data);
        } catch (e) {
            console.error(e);
        }
    }, [taskId, fetchActivity]);

    useWebSocket(wsUrl(), (msg) => {
        if (msg.type === 'comment_created' && msg.payload.task_id === taskId) {
            setComments(prev => {
                // deduplicate: ignore if we already have this comment (e.g. from a recent re-fetch)
                if (prev.some((c: any) => c.id === msg.payload.id)) return prev;
                return [...prev, msg.payload];
            });
        }
        if (msg.type === 'artifact_created') {
            // Artifacts can be produced by delegated subtask sessions (their
            // own task ids), so refetch the whole tree instead of filtering.
            axios.get(`/api/tasks/${taskId}/artifacts`).then(res => setArtifacts(res.data || [])).catch(() => {});
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
            if (runIsOurs) {
                // Only clear the running marker when it was OUR run that ended —
                // unrelated runs finishing elsewhere must not make this task
                // look idle.
                setTask((prev: any) => prev && prev.run_id === msg.payload.run_id ? { ...prev, run_id: null } : prev);
                // Re-sync after run completes to catch any comments/artifacts whose WS events may have been missed
                fetchActivity();
            }
        }
        if (msg.type === 'run_log') {
            if (msg.payload.entry) {
                setRuns(prev => prev.map((r: any) => r.id === msg.payload.run_id ? { ...r, log_entries: [...(r.log_entries || []), msg.payload.entry] } : r));
            }
        }
    }, { enabled: !!taskId, onConnect: resyncAfterReconnect });

    const handleAddComment = async () => {
        if (!newComment.trim() || !taskId) return;
        const content = newComment.trim();
        const hasPendingHumanQuestion = comments.some((question: any) =>
            ['ask_user', 'ask_owner'].includes(question.comment_type) &&
            question.author_type === 'agent' &&
            !comments.some((answer: any) =>
                answer.id > question.id && answer.author_type === 'human' &&
                !['ask_user', 'ask_owner', 'status_change', 'artifact_created'].includes(answer.comment_type),
            ),
        );
        setIsPostingComment(true);
        setCommentError('');
        try {
            const response = await axios.post('/api/comments', {
                task_id: taskId,
                author_type: 'human',
                content,
                run_agent: task?.status === 'blocked' || hasPendingHumanQuestion ? false : runAgent
            });
            setNewComment('');
            setComments(prev => prev.some((c: any) => c.id === response.data?.id) ? prev : [...prev, response.data]);
            // The reply changes task/run state asynchronously. Re-read both
            // streams so the modal immediately leaves the human-wait state,
            // even when the websocket event was missed during the transition.
            await fetchActivity();
            const taskRes = await axios.get(`/api/tasks/${taskId}`);
            setTask((prev: any) => prev ? { ...prev, ...taskRes.data } : taskRes.data);
            setFormData(prev => ({
                ...prev,
                status: taskRes.data.status,
                is_archived: taskRes.data.is_archived,
            }));
        } catch (e: any) {
            console.error(e);
            setCommentError(e.response?.data?.error || 'Could not submit the reply. Please try again.');
        } finally {
            setIsPostingComment(false);
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
				git_base_branch: formData.git_base_branch,
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

    const hasPendingHumanQuestion = comments.some((question: any) =>
        ['ask_user', 'ask_owner'].includes(question.comment_type) &&
        question.author_type === 'agent' &&
        !comments.some((answer: any) =>
            answer.id > question.id && answer.author_type === 'human' &&
            !['ask_user', 'ask_owner', 'status_change', 'artifact_created'].includes(answer.comment_type),
        ),
    );

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
                    <span className="text-xs font-mono text-gray-400">{task.ref_key || `${prefix}-${task.id}`}{formData.is_archived ? <span className="ml-2 bg-red-100 text-red-800 px-1.5 py-0.5 rounded">Archived</span> : null}</span>
                    <h1 className="text-xl font-bold text-gray-900 truncate">{formData.title || task.title}</h1>
					{task.github_pr_url && <a href={task.github_pr_url} target="_blank" rel="noreferrer" className="text-sm text-indigo-600 hover:underline">PR #{task.github_pr_number}</a>}
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
                        {task?.parent_id && (
                            <div className="mb-4 flex items-start gap-2 border border-violet-200 bg-violet-50 text-violet-900 px-3 py-2 rounded-lg text-xs" data-testid="subtask-banner">
                                <span className="mt-0.5">🧩</span>
                                <div>
                                    <span className="font-semibold">Delegated subtask</span> of{' '}
                                    <Link to={`/companies/${shortName}/tasks/${task.parent_id}`} className="font-medium text-violet-700 underline hover:text-violet-900">
                                        task #{task.parent_id}
                                    </Link>
                                    . Runs listed here are sub-sessions of the parent task's main run — re-running restarts the parent's main session.
                                </div>
                            </div>
                        )}
                        <form id="task-form" onSubmit={handleSaveTask} className="space-y-4">
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Title</label>
                                <input required type="text" value={formData.title} onChange={e => setFormData({...formData, title: e.target.value})} className="w-full border rounded p-2 shadow-sm" placeholder="Task title" />
                            </div>
                            <div>
                                <label className="block text-sm font-medium text-gray-700 mb-1">Description <span className="font-normal text-gray-400">(user input)</span></label>
                                <textarea rows={5} value={formData.description} onChange={e => setFormData({...formData, description: e.target.value})} className="w-full border rounded p-2 shadow-sm" placeholder="Task details..." />
                            </div>
                        </form>

                        {/* CEO-generated spec: kept separate from the user's original input.
                            The refined description stays visible next to the user input;
                            acceptance criteria and test cases are collapsed by default. */}
                        {task && (task.refined_description || task.acceptance_criteria || task.test_cases) && (
                            <div className="mt-4 space-y-3" data-testid="ceo-spec">
                                {task.refined_description && (
                                    <div className="border border-violet-200 rounded-lg bg-violet-50/40 overflow-hidden">
                                        <div className="flex items-center justify-between px-3 py-1.5 bg-violet-50 border-b border-violet-100">
                                            <span className="text-xs font-semibold text-violet-800">Refined Description</span>
                                                <span className="text-xs bg-violet-100 text-violet-700 px-2 py-0.5 rounded-full" title="This field was produced by the CEO orchestrator during planning">
                                                🤖 Generated by CEO
                                            </span>
                                        </div>
                                        <div className="px-3 py-2 bg-white prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0 text-sm">
                                            <ReactMarkdown remarkPlugins={[remarkGfm]}>{task.refined_description}</ReactMarkdown>
                                        </div>
                                    </div>
                                )}
                                {([
                                    ['Acceptance Criteria', task.acceptance_criteria],
                                    ['Test Cases', task.test_cases],
                                ] as [string, string][]).filter(([, value]) => value).map(([label, value]) => {
                                    const isExpanded = expandedSpecs.has(label);
                                    const items = parseSpecItems(value);
                                    const passed = items ? items.filter((it: any) => it.status === 'passed').length : 0;
                                    const failed = items ? items.filter((it: any) => it.status === 'failed').length : 0;
                                    return (
                                        <div key={label} className="border border-violet-200 rounded-lg bg-violet-50/40 overflow-hidden">
                                            <button
                                                type="button"
                                                onClick={() => setExpandedSpecs(prev => {
                                                    const next = new Set(prev);
                                                    if (next.has(label)) next.delete(label); else next.add(label);
                                                    return next;
                                                })}
                                                className="w-full flex items-center justify-between px-3 py-1.5 bg-violet-50 hover:bg-violet-100 transition-colors text-left"
                                            >
                                                <span className="flex items-center gap-1.5 text-xs font-semibold text-violet-800">
                                                    {isExpanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
                                                    {label}
                                                    {items && (
                                                        <span className={`font-normal px-1.5 py-0.5 rounded-full ${
                                                            failed > 0 ? 'bg-red-100 text-red-700'
                                                            : passed === items.length ? 'bg-green-100 text-green-700'
                                                            : 'bg-gray-100 text-gray-600'
                                                        }`}>
                                                            {passed + failed > 0 ? `${passed}/${items.length} passed${failed > 0 ? `, ${failed} failed` : ''}` : `${items.length} items`}
                                                        </span>
                                                    )}
                                                </span>
                                            <span className="text-xs bg-violet-100 text-violet-700 px-2 py-0.5 rounded-full" title="This field was produced by the CEO orchestrator during planning">
                                                    🤖 Generated by CEO
                                                </span>
                                            </button>
                                            {isExpanded && (
                                                items ? (
                                                    <ul className="bg-white border-t border-violet-100 divide-y divide-gray-50">
                                                        {items.map((it: any) => (
                                                            <li key={it.id} className="flex items-start gap-2 px-3 py-1.5 text-sm">
                                                                <span className="mt-0.5 shrink-0" title={it.status}>
                                                                    {it.status === 'passed' ? '✅' : it.status === 'failed' ? '❌' : '⬜'}
                                                                </span>
                                                                <span className="min-w-0">
                                                                    <span className="text-gray-800">{it.text}</span>
                                                                    {it.note && <span className="ml-2 text-xs text-gray-500 italic">{it.note}</span>}
                                                                </span>
                                                            </li>
                                                        ))}
                                                    </ul>
                                                ) : (
                                                    // Legacy plain-text spec from before structured items.
                                                    <div className="px-3 py-2 bg-white border-t border-violet-100 prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0 text-sm">
                                                        <ReactMarkdown remarkPlugins={[remarkGfm]}>{value}</ReactMarkdown>
                                                    </div>
                                                )
                                            )}
                                        </div>
                                    );
                                })}
                            </div>
                        )}

                        {/* Artifacts: task-level deliverables, minimized by default */}
                        {taskId && artifacts.length > 0 && (
                            <div className="mt-4 border border-emerald-200 rounded-lg bg-emerald-50/40 overflow-hidden" data-testid="artifacts-block">
                                <div className="flex items-center justify-between px-3 py-1.5 bg-emerald-50">
                                    <button
                                        type="button"
                                        onClick={() => setArtifactsExpanded(v => !v)}
                                        className="flex items-center gap-1.5 text-xs font-semibold text-emerald-800 hover:text-emerald-900"
                                    >
                                        {artifactsExpanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
                                        📄 {artifacts.length} artifact{artifacts.length > 1 ? 's' : ''}
                                    </button>
                                    <a
                                        href={`/api/tasks/${taskId}/artifacts/download`}
                                        className="text-xs text-emerald-700 hover:underline"
                                        title="Download all artifacts as a zip archive"
                                    >
                                        ⬇ Download all
                                    </a>
                                </div>
                                {artifactsExpanded && (
                                    <ul className="bg-white border-t border-emerald-100 divide-y divide-gray-50">
                                        {artifacts.map((a: any) => {
                                            const isOpen = expandedArtifactIds.has(a.id);
                                            return (
                                                <li key={a.id}>
                                                    <div className="flex items-center gap-2 px-3 py-1.5 text-xs">
                                                        <button
                                                            type="button"
                                                            onClick={() => setExpandedArtifactIds(prev => {
                                                                const next = new Set(prev);
                                                                if (next.has(a.id)) next.delete(a.id); else next.add(a.id);
                                                                return next;
                                                            })}
                                                            className="flex items-center gap-1.5 min-w-0 flex-1 text-left hover:text-emerald-800"
                                                        >
                                                            {isOpen ? <ChevronUp size={11} /> : <ChevronDown size={11} />}
                                                            <span className="font-semibold text-gray-800 truncate">{a.filename}</span>
                                                            <span className="text-gray-400 shrink-0">{(a.content || '').length} bytes</span>
                                                            {a.is_verified && (
                                                                <span className="bg-green-100 text-green-700 px-1.5 py-0.5 rounded-full shrink-0">✓ verified</span>
                                                            )}
                                                            {a.description && <span className="text-gray-500 italic truncate">{a.description}</span>}
                                                        </button>
                                                        <a
                                                            href={`/api/artifacts/${a.id}/download`}
                                                            className="text-emerald-700 hover:underline shrink-0"
                                                            title={`Download ${a.filename}`}
                                                        >
                                                            ⬇
                                                        </a>
                                                    </div>
                                                    {isOpen && (
                                                        <div className="px-3 pb-2 border-t border-gray-50">
                                                            <div className="prose prose-sm max-w-none mt-2 prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0 text-sm max-h-64 overflow-y-auto">
                                                                <ReactMarkdown remarkPlugins={[remarkGfm]}>{a.content || ''}</ReactMarkdown>
                                                            </div>
                                                        </div>
                                                    )}
                                                </li>
                                            );
                                        })}
                                    </ul>
                                )}
                            </div>
                        )}

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

                                        // Keep answers visually attached to the question that caused
                                        // them, like a small messenger thread. Human questions use
                                        // ask_user; worker questions routed through the orchestrator
                                        // use ask_owner and are answered by the next agent update.
                                        const humanReplyByQuestion = new Map<number, any>();
                                        const pairedHumanReplyIds = new Set<number>();
                                        comments.filter((comment: any) => ['ask_user', 'ask_owner'].includes(comment.comment_type)).forEach((question: any) => {
                                            const expectedAuthor = question.comment_type === 'ask_user' ? 'human' : 'agent';
                                            const reply = comments
                                                .filter((comment: any) => {
                                                    if (comment.id <= question.id || comment.author_type !== expectedAuthor) return false;
                                                    if (question.comment_type === 'ask_owner' && comment.run_id === question.run_id) return false;
                                                    return !['ask_user', 'ask_owner', 'status_change', 'artifact_created'].includes(comment.comment_type);
                                                })
                                                .sort((a: any, b: any) => a.id - b.id)[0];
                                            if (reply) {
                                                humanReplyByQuestion.set(question.id, reply);
                                                pairedHumanReplyIds.add(reply.id);
                                            }
                                        });

                                        if (timeline.length === 0) {
                                            return <p className="text-sm text-gray-500 italic">No activity yet.</p>;
                                        }

                                        return timeline.map((item) => {
                                            if (item.type === 'comment') {
                                                const c = item.data;
                                                if (pairedHumanReplyIds.has(c.id)) return null;
                                                const ts = new Date(c.created_at);
                                                const timeStr = ts.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
                                                const isStatusChange = c.comment_type === 'status_change';
                                                const isArtifact = c.comment_type === 'artifact_created';
                                                const isTaskDone = c.comment_type === 'task_done';
                                                const isAskQuestion = ['ask_user', 'ask_owner'].includes(c.comment_type);
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
                                                        'blocked': 'Blocked', 'depends-on-task': 'Depends on Task',
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

                                                if (isAskQuestion) {
                                                    const reply = humanReplyByQuestion.get(c.id);
                                                    return (
                                                        <div key={`dialogue-${c.id}`} className="max-w-[92%] space-y-2" data-testid="question-dialogue">
                                                            <div className="rounded-lg border border-indigo-200 bg-indigo-50 p-3 text-sm text-gray-800">
                                                                <div className="mb-1 flex items-center gap-2 text-xs font-bold text-gray-500">
                                                                    <span>{getActivityAuthorLabel(c, runs)}</span>
                                                                    <span className="font-normal ml-auto">{timeStr}</span>
                                                                </div>
                                                                <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1">
                                                                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{c.content}</ReactMarkdown>
                                                                </div>
                                                            </div>
                                                            {reply ? (
                                                                <div className="ml-8 rounded-lg border border-gray-200 bg-gray-100 p-3 text-sm text-gray-900">
                                                                    <div className="mb-1 flex items-center gap-2 text-xs font-bold text-gray-500">
                                                                        <span>{getActivityAuthorLabel(reply, runs)}</span>
                                                                        <span className="font-normal ml-auto">{new Date(reply.created_at).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</span>
                                                                    </div>
                                                                    <span className="whitespace-pre-wrap">{reply.content}</span>
                                                                </div>
                                                            ) : (
                                                                <div className="ml-8 text-xs italic text-amber-700">Waiting for a reply…</div>
                                                            )}
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
                                                else if (isAskQuestion) bubbleClass = 'bg-amber-50 border border-amber-200 text-gray-800';
                                                else if (!isAgent) bubbleClass = 'bg-gray-200 text-gray-900';

                                                const authorLabel = getActivityAuthorLabel(c, runs);

                                                const statusLabel: Record<string, string> = {
                                                    'to-do': 'To Do', 'in-progress': 'In Progress',
                                                    'in-review': 'In Review', 'done': 'Done',
                                                    'blocked': 'Blocked', 'depends-on-task': 'Depends on Task',
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
                                                                <span className="font-semibold text-gray-600 flex items-center gap-1.5">
                                                                    ⚙️ Run {r.name || `#${r.id}`}
                                                                    {r.parent_run_id ? (
                                                                        <span className="font-normal bg-violet-100 text-violet-700 px-1.5 py-0.5 rounded-full" title={`Sub-session of run #${r.parent_run_id}`}>sub-session</span>
                                                                    ) : (
                                                                        <span className="font-normal bg-indigo-50 text-indigo-600 px-1.5 py-0.5 rounded-full">main session</span>
                                                                    )}
                                                                    {getRunAgentName(r) && (
                                                                        <span className="font-normal bg-gray-100 text-gray-600 px-1.5 py-0.5 rounded-full" title={`Agent: ${getRunAgentName(r)}`}>
                                                                            {getRunAgentName(r)}
                                                                        </span>
                                                                    )}
                                                                    {r.title && (
                                                                        <span className="font-normal bg-slate-50 text-slate-600 px-1.5 py-0.5 rounded-full max-w-[18rem] truncate" title={`Session purpose: ${r.title}`}>
                                                                            {r.title}
                                                                        </span>
                                                                    )}
                                                                </span>
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
                                                                    agentName={getRunAgentName(r)}
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
                                    {task?.run_id && task?.status !== 'blocked' && !hasPendingHumanQuestion ? (
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
                                        <>
                                        <div className="flex gap-2" data-testid="human-reply-form">
                                            <input
                                                type="text"
                                                value={newComment}
                                                onChange={(e) => setNewComment(e.target.value)}
                                                onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void handleAddComment(); } }}
                                                placeholder="Add a comment..."
                                                className="flex-1 border-gray-300 rounded-md shadow-sm border p-2 focus:ring-indigo-500 focus:border-indigo-500 text-sm"
                                                disabled={isPostingComment}
                                            />
                                            {task?.status === 'blocked' ? (
                                                <span className="flex items-center px-2 text-xs text-amber-700">Answer pending question</span>
                                            ) : (
                                                <div className="flex items-center px-2">
                                                    <input
                                                        type="checkbox"
                                                        id="runAgentCheckbox"
                                                        checked={runAgent}
                                                        onChange={(e) => setRunAgent(e.target.checked)}
                                                    />
                                                    <label htmlFor="runAgentCheckbox" className="ml-1 text-xs text-gray-600">Run Agent</label>
                                                </div>
                                            )}
                                            <button type="button" onClick={handleAddComment} disabled={isPostingComment || !newComment.trim()} className="bg-indigo-600 text-white p-2 rounded-md hover:bg-indigo-700 disabled:opacity-50">
                                                <Send size={18} />
                                            </button>
                                        </div>
                                        {commentError && <p className="mt-1 text-xs text-red-600">{commentError}</p>}
                                        </>
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
                                <label htmlFor="task-status" className="block text-sm font-medium text-gray-700 mb-1">Status</label>
                                <select id="task-status" value={formData.status} onChange={e => setFormData({...formData, status: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm font-semibold text-indigo-600">
                                    <option value="backlog">Backlog</option>
                                    <option value="to-do">To Do</option>
                                    <option value="in-progress">In Progress</option>
                                    <option value="in-review">In Review</option>
                                    <option value="blocked">Blocked</option>
                                    <option value="depends-on-task" disabled>Depends on Task</option>
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
						{formData.project_id && (
							<div className="rounded-lg border border-gray-200 bg-gray-50/70">
								<button
									type="button"
									onClick={() => setGitOptionsExpanded(value => !value)}
									className="w-full px-3 py-2.5 flex items-center justify-between gap-3 text-left text-sm hover:bg-gray-100 rounded-lg"
									aria-expanded={gitOptionsExpanded}
								>
									<span className="font-medium text-gray-700">Git options</span>
									<span className="flex items-center gap-1.5 text-xs text-gray-500">
										Base: <code className="font-mono text-gray-700">{formData.git_base_branch}</code>
										{gitOptionsExpanded ? <ChevronUp size={15} /> : <ChevronDown size={15} />}
									</span>
								</button>
								{gitOptionsExpanded && (
									<div className="border-t border-gray-200 px-3 pb-3 pt-2.5">
										<label className="block text-sm font-medium text-gray-700 mb-1">Base branch</label>
										<select
											value={formData.git_base_branch}
											disabled={branchesLoading || ['in-progress', 'in-review', 'done'].includes(formData.status)}
											onChange={e => setFormData({...formData, git_base_branch: e.target.value})}
											className="w-full border rounded p-2 text-sm shadow-sm disabled:bg-gray-100 disabled:text-gray-500"
										>
											{projectBranches.map(branch => <option key={branch} value={branch}>{branch}</option>)}
										</select>
										<p className="mt-1 text-xs text-gray-500">
											{['in-progress', 'in-review', 'done'].includes(formData.status)
												? 'The base branch is locked after work starts.'
												: branchesLoading ? 'Loading repository branches…' : 'New worktree and pull request start from this branch.'}
										</p>
									</div>
								)}
							</div>
						)}
                        <div>
                            <label htmlFor="task-sprint" className="block text-sm font-medium text-gray-700 mb-1">Sprint</label>
                            <select id="task-sprint" required value={formData.sprint_id} onChange={e => setFormData({...formData, sprint_id: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm">
                                <option value="" disabled>-- Select Sprint --</option>
                                {sprints.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}
                            </select>
                        </div>
                        <div>
                            <label htmlFor="task-assignee" className="block text-sm font-medium text-gray-700 mb-1">Assignee</label>
                            <select id="task-assignee" value={formData.agent_id} onChange={e => setFormData({...formData, agent_id: e.target.value})} className="w-full border rounded p-2 text-sm shadow-sm">
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
                                            <span className="text-xs text-gray-500 font-mono mr-2">{t.ref_key || `${prefix}-${t.id}`}</span>
                                            {t.title}
                                        </div>
                                    ))}
                                </div>
                            )}
                        </div>
                        {taskId && <TaskRelations taskId={taskId} allTasks={allTasks} />}
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
