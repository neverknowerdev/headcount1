import React, { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { PhaseChip } from './PhaseChip';
import { buildTaskForest, type TaskNode } from '../utils/taskHierarchy';
import { formatDateTime, formatTokens, statusLabel } from '../lib/workflow';

// A task as a list of sessions shows it, as the API sends it with tree=true.
export interface RunTask {
    id: number;
    parent_id?: number | null;
    root_task_id: number;
    ref_key: string;
    title: string;
    task_type: string;
    status: string;
    phase: string;
    waiting_on: string;
    wait_detail: string;
    result_reason?: string;
}

// An executor session in overview: no transcript.
export interface RunRow {
    id: number;
    task_id: number;
    name?: string;
    status: string;
    attempt?: number;
    started_at?: string;
    ended_at?: string | null;
    agent_name?: string;
    agent?: { id?: number; name?: string };
    token_stats?: { total_tokens?: number };
}

export interface RunTreeData {
    runs: RunRow[];
    tasks: RunTask[];
}

const SESSION_STYLE: Record<string, string> = {
    running: 'bg-indigo-50 text-indigo-700',
    paused: 'bg-amber-50 text-amber-800',
    resuming: 'bg-amber-50 text-amber-800',
    completed: 'bg-emerald-50 text-emerald-700',
    failed: 'bg-red-50 text-red-700',
    canceled: 'bg-slate-100 text-slate-600',
};

const TASK_STATUS_STYLE: Record<string, string> = {
    'in-progress': 'bg-indigo-50 text-indigo-700',
    blocked: 'bg-rose-50 text-rose-700',
    'in-review': 'bg-cyan-50 text-cyan-700',
    done: 'bg-emerald-50 text-emerald-700',
    failed: 'bg-red-50 text-red-700',
    canceled: 'bg-slate-100 text-slate-500',
};

const duration = (run: RunRow): string => {
    if (!run.started_at) return '';
    const start = new Date(run.started_at);
    if (start.getFullYear() <= 1) return '';
    const seconds = Math.max(0, Math.floor(((run.ended_at ? new Date(run.ended_at) : new Date()).getTime() - start.getTime()) / 1000));
    if (seconds < 60) return `${seconds}s`;
    const minutes = Math.floor(seconds / 60);
    return minutes < 60 ? `${minutes}m ${seconds % 60}s` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
};

// A tree with no more sessions than this is shown open to the last one.
const OPEN_TREE_UP_TO = 12;

interface Tally {
    sessions: number;
    failed: number;
    running: number;
    latest: number;
}

// RunTree shows executor sessions the way their tasks are arranged: each
// top-level task, its subtasks beneath it, and under every task the sessions
// that worked on it. A task's line says how many sessions ran beneath it and
// how many failed, so a tree does not have to be opened to see where the
// trouble is.
export const RunTree: React.FC<{ data: RunTreeData; companyPath: string; emptyText?: string }> = ({ data, companyPath, emptyText }) => {
    const { forest, runsByTask, tallies } = useMemo(() => {
        const runsByTask = new Map<number, RunRow[]>();
        for (const run of data.runs) {
            if (!runsByTask.has(run.task_id)) runsByTask.set(run.task_id, []);
            runsByTask.get(run.task_id)!.push(run);
        }
        for (const runs of runsByTask.values()) runs.sort((a, b) => a.id - b.id);
        const forest = buildTaskForest([...data.tasks].sort((a, b) => a.id - b.id));
        const tallies = new Map<number, Tally>();
        const tally = (node: TaskNode<RunTask>): Tally => {
            const total: Tally = { sessions: 0, failed: 0, running: 0, latest: 0 };
            for (const run of runsByTask.get(node.task.id) || []) {
                total.sessions++;
                if (run.status === 'failed') total.failed++;
                if (run.status === 'running' || run.status === 'resuming') total.running++;
                total.latest = Math.max(total.latest, run.started_at ? new Date(run.started_at).getTime() : 0);
            }
            for (const child of node.children) {
                const beneath = tally(child);
                total.sessions += beneath.sessions;
                total.failed += beneath.failed;
                total.running += beneath.running;
                total.latest = Math.max(total.latest, beneath.latest);
            }
            tallies.set(node.task.id, total);
            return total;
        };
        forest.forEach(tally);
        // The tree worked on most recently comes first.
        forest.sort((a, b) => (tallies.get(b.task.id)?.latest || 0) - (tallies.get(a.task.id)?.latest || 0));
        return { forest, runsByTask, tallies };
    }, [data]);

    // Top-level tasks start open, so the first level of each tree is in view.
    // A small tree is open all the way down; in a large one a subtask's
    // sessions open on request, and its line already says how they went.
    const [toggled, setToggled] = useState<Set<number>>(() => new Set());
    const isOpen = (node: TaskNode<RunTask>, depth: number, treeSessions: number): boolean =>
        (depth === 0 || treeSessions <= OPEN_TREE_UP_TO) !== toggled.has(node.task.id);
    const toggle = (id: number) => setToggled(current => {
        const next = new Set(current);
        if (next.has(id)) next.delete(id); else next.add(id);
        return next;
    });

    const renderNode = (node: TaskNode<RunTask>, depth: number, treeSessions: number): React.ReactNode => {
        const { task } = node;
        const own = runsByTask.get(task.id) || [];
        const total = tallies.get(task.id) || { sessions: 0, failed: 0, running: 0, latest: 0 };
        const open = isOpen(node, depth, treeSessions);
        return (
            <li key={task.id} data-testid="run-tree-task" data-depth={depth}>
                <div className={`flex flex-wrap items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-gray-50 ${depth === 0 ? 'bg-gray-50' : ''}`}>
                    <button
                        type="button"
                        onClick={() => toggle(task.id)}
                        aria-expanded={open}
                        aria-label={`${open ? 'Collapse' : 'Expand'} ${task.ref_key || task.title}`}
                        className="grid size-5 shrink-0 place-items-center rounded text-gray-500 hover:bg-gray-200"
                    >
                        {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                    </button>
                    <span className="shrink-0 font-mono text-xs text-gray-500">{task.ref_key || `#${task.id}`}</span>
                    <Link to={`${companyPath}/tasks/${task.id}`} className={`min-w-0 max-w-xl truncate hover:text-indigo-600 hover:underline ${depth === 0 ? 'font-semibold text-gray-900' : 'text-gray-800'}`} title={task.title}>
                        {task.title}
                    </Link>
                    <span className={`shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium ${TASK_STATUS_STYLE[task.status] || 'bg-slate-100 text-slate-600'}`}>{statusLabel(task.status)}</span>
                    <PhaseChip task={task} />
                    <span className="ml-auto shrink-0 text-xs text-gray-500" data-testid="run-tree-tally">
                        {total.sessions} {total.sessions === 1 ? 'session' : 'sessions'}
                        {total.running > 0 && <span className="text-indigo-600"> · {total.running} running</span>}
                        {total.failed > 0 && <span className="text-red-600"> · {total.failed} failed</span>}
                    </span>
                </div>
                {open && (
                    <ul className="ml-4 border-l border-gray-200 pl-3">
                        {own.map(run => (
                            <li key={`run-${run.id}`} data-testid="run-card">
                                <Link to={`${companyPath}/run-logs/${run.id}`} className="flex flex-wrap items-center gap-2 rounded px-2 py-1 text-sm hover:bg-indigo-50">
                                    <span className="font-mono text-xs text-indigo-700">{run.name || `session #${run.id}`}</span>
                                    {(run.attempt || 0) > 1 && <span className="rounded-full bg-amber-50 px-1.5 py-0.5 text-[11px] text-amber-800">attempt {run.attempt}</span>}
                                    <span className="text-xs text-gray-600">{run.agent?.name || run.agent_name || ''}</span>
                                    <span className={`rounded-full px-2 py-0.5 text-[11px] font-medium ${SESSION_STYLE[run.status] || 'bg-slate-100 text-slate-600'}`}>{run.status}</span>
                                    <span className="ml-auto text-xs text-gray-400">
                                        {(run.token_stats?.total_tokens || 0) > 0 && `${formatTokens(run.token_stats!.total_tokens!)} tok · `}
                                        {duration(run)}{duration(run) && ' · '}{formatDateTime(run.started_at)}
                                    </span>
                                </Link>
                            </li>
                        ))}
                        {node.children.map(child => renderNode(child, depth + 1, treeSessions))}
                    </ul>
                )}
            </li>
        );
    };

    if (data.runs.length === 0) {
        return <p className="text-sm italic text-gray-500">{emptyText || 'No executor sessions recorded yet.'}</p>;
    }
    return <ul className="space-y-2" data-testid="run-tree">{forest.map(node => renderNode(node, 0, tallies.get(node.task.id)?.sessions || 0))}</ul>;
};
