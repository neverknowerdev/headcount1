import React from 'react';
import { Link } from 'react-router-dom';
import { PhaseChip, TaskTypeBadge } from './PhaseChip';
import { phaseLabel, statusLabel } from '../lib/workflow';
import type { Decision, Task } from '../lib/workflow';
import type { RunRow } from './RunTree';

const STATUS_DOT: Record<string, string> = {
    done: 'bg-green-500',
    'in-review': 'bg-emerald-400',
    'in-progress': 'bg-indigo-500',
    'to-do': 'bg-indigo-300',
    'depends-on-task': 'bg-gray-300',
    blocked: 'bg-amber-500',
    failed: 'bg-red-500',
    canceled: 'bg-gray-400',
    backlog: 'bg-gray-300',
};

// childrenOf groups a flat task list (a task and everything beneath it) by parent.
function childrenOf(tasks: Task[]): Map<number, Task[]> {
    const byParent = new Map<number, Task[]>();
    for (const task of tasks) {
        if (task.parent_id == null) continue;
        if (!byParent.has(task.parent_id)) byParent.set(task.parent_id, []);
        byParent.get(task.parent_id)!.push(task);
    }
    return byParent;
}

const KIND_STYLE: Record<string, { label: string; className: string }> = {
    decision: { label: 'Decided', className: 'bg-indigo-100 text-indigo-700' },
    assumption: { label: 'Assumed', className: 'bg-amber-100 text-amber-800' },
    dead_end: { label: 'Dead end', className: 'bg-red-100 text-red-700' },
};

const DecisionRow: React.FC<{ decision: Decision; superseded: boolean }> = ({ decision, superseded }) => {
    const kind = KIND_STYLE[decision.kind] || { label: decision.kind, className: 'bg-gray-100 text-gray-600' };
    return (
        <li data-testid="decision" className={`rounded border border-gray-100 bg-white px-3 py-2 text-sm ${superseded ? 'opacity-60' : ''}`}>
            <div className="flex items-start gap-2">
                <span className={`flex-shrink-0 rounded px-1.5 py-0.5 text-[11px] font-medium ${kind.className}`}>{kind.label}</span>
                <span className={`min-w-0 flex-1 font-medium text-gray-900 ${superseded ? 'line-through' : ''}`}>{decision.title}</span>
                {decision.phase && <span className="flex-shrink-0 text-[11px] text-gray-400">{phaseLabel(decision.phase)}</span>}
            </div>
            {decision.decision && decision.decision !== decision.title && <p className="mt-1 whitespace-pre-wrap text-gray-700">{decision.decision}</p>}
            {decision.rationale && <p className="mt-1 whitespace-pre-wrap text-xs text-gray-500"><span className="font-medium">Why:</span> {decision.rationale}</p>}
            {decision.alternatives && <p className="mt-1 whitespace-pre-wrap text-xs text-gray-500"><span className="font-medium">Instead of:</span> {decision.alternatives}</p>}
            {superseded && <p className="mt-1 text-xs italic text-gray-400">Later revised.</p>}
        </li>
    );
};

interface TreeProps {
    // The task at the top and everything beneath it, parents before children.
    tasks: Task[];
    companyPath: string;
    // With decisions the tree shows what was decided on each task instead of
    // each task's progress.
    decisions?: Decision[];
    // The executor sessions of the tasks in the tree, listed under the task
    // each one worked on.
    runs?: RunRow[];
}

// TaskTree draws a task and the subtasks its workflow created, nested the way
// they were delegated. Given decisions, it becomes the decision tree: every
// choice, assumption and dead end under the task that made it.
export const TaskTree: React.FC<TreeProps> = ({ tasks, companyPath, decisions, runs }) => {
    if (tasks.length === 0) return null;
    const runsByTask = new Map<number, RunRow[]>();
    for (const run of runs || []) {
        if (!runsByTask.has(run.task_id)) runsByTask.set(run.task_id, []);
        runsByTask.get(run.task_id)!.push(run);
    }
    for (const list of runsByTask.values()) list.sort((a, b) => a.id - b.id);
    const byParent = childrenOf(tasks);
    const byID = new Map(tasks.map(task => [task.id, task]));
    const superseded = new Set((decisions || []).map(d => d.supersedes_id).filter((id): id is number => id != null));
    const decisionsByTask = new Map<number, Decision[]>();
    for (const decision of decisions || []) {
        if (!decisionsByTask.has(decision.task_id)) decisionsByTask.set(decision.task_id, []);
        decisionsByTask.get(decision.task_id)!.push(decision);
    }
    // A task counts if it, or anything beneath it, decided something.
    const hasDecisions = (task: Task): boolean =>
        (decisionsByTask.get(task.id)?.length || 0) > 0 || (byParent.get(task.id) || []).some(hasDecisions);

    const render = (task: Task, isRoot: boolean): React.ReactNode => {
        if (decisions && !hasDecisions(task)) return null;
        const children = byParent.get(task.id) || [];
        const dependsOn = (task.relation_summary?.depends_on || [])
            .map(dep => byID.get(dep.id))
            .filter((dep): dep is Task => !!dep);
        return (
            <li key={task.id} data-testid="tree-task">
                {!(isRoot && !decisions) && (
                    <div className="flex flex-wrap items-center gap-2 py-1 text-sm">
                        <span className={`h-2 w-2 flex-shrink-0 rounded-full ${STATUS_DOT[task.status] || 'bg-gray-300'}`} title={statusLabel(task.status)} />
                        <Link to={`${companyPath}/tasks/${task.id}`} className="min-w-0 truncate font-medium text-gray-900 hover:text-indigo-600 hover:underline">
                            {task.ref_key ? `${task.ref_key} ` : ''}{task.title}
                        </Link>
                        {!decisions && (
                            <>
                                <TaskTypeBadge type={task.task_type} />
                                <span className="text-xs text-gray-500">{statusLabel(task.status)}</span>
                                <PhaseChip task={task} />
                                {task.origin_phase && <span className="text-[11px] text-gray-400">from {phaseLabel(task.origin_phase).toLowerCase()}</span>}
                                {dependsOn.length > 0 && (
                                    <span className="text-[11px] text-gray-400" data-testid="tree-task-after">
                                        after {dependsOn.map(dep => dep.ref_key || `#${dep.id}`).join(', ')}
                                    </span>
                                )}
                            </>
                        )}
                    </div>
                )}
                {!decisions && !isRoot && task.result_summary && (
                    <p className="mb-1 ml-4 whitespace-pre-wrap text-xs text-gray-600">{task.result_summary}</p>
                )}
                {!decisions && (runsByTask.get(task.id)?.length || 0) > 0 && (
                    <p className="mb-1 ml-4 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-gray-500" data-testid="tree-task-sessions">
                        <span>Sessions:</span>
                        {runsByTask.get(task.id)!.map(run => (
                            <Link key={run.id} to={`${companyPath}/run-logs/${run.id}`} className={`font-mono hover:underline ${run.status === 'failed' ? 'text-red-600' : 'text-indigo-700'}`}>
                                {run.name || `#${run.id}`}{run.status !== 'completed' ? ` (${run.status})` : ''}
                            </Link>
                        ))}
                    </p>
                )}
                {decisions && (decisionsByTask.get(task.id)?.length || 0) > 0 && (
                    <ul className="mb-1 ml-4 space-y-1">
                        {decisionsByTask.get(task.id)!.map(decision => (
                            <DecisionRow key={decision.id} decision={decision} superseded={superseded.has(decision.id)} />
                        ))}
                    </ul>
                )}
                {children.length > 0 && (
                    <ul className={isRoot && !decisions ? '' : 'ml-3 border-l border-gray-200 pl-3'}>
                        {children.map(child => render(child, false))}
                    </ul>
                )}
            </li>
        );
    };

    const root = tasks[0];
    if (!decisions && (byParent.get(root.id) || []).length === 0) {
        return <p className="text-sm italic text-gray-400">No subtasks yet.</p>;
    }
    if (decisions && decisions.length === 0) {
        return <p className="text-sm italic text-gray-400">No decisions recorded yet.</p>;
    }
    return <ul data-testid={decisions ? 'decision-tree' : 'subtask-tree'}>{render(root, true)}</ul>;
};
