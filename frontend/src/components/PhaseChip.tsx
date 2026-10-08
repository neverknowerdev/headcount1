import React from 'react';
import { phaseLabel, waitLabel, waitNeedsAttention, taskTypeLabel, RESULT_REASONS } from '../lib/workflow';
import type { Task } from '../lib/workflow';

type ChipTask = Pick<Task, 'status' | 'phase' | 'waiting_on' | 'wait_detail' | 'result_reason'>;

// PhaseChip says where a task is in its workflow: the step it is in and, if it
// is not moving, what it is waiting for. It renders nothing for a task that
// has not started or has simply finished.
export const PhaseChip: React.FC<{ task: ChipTask; className?: string }> = ({ task, className }) => {
    const working = task.status === 'in-progress' || task.status === 'blocked';
    if (!working) {
        if ((task.status === 'failed' || task.status === 'canceled') && task.result_reason) {
            return (
                <span data-testid="phase-chip" className={`inline-flex items-center rounded-full bg-red-50 px-2 py-0.5 text-xs text-red-700 ${className || ''}`}>
                    {RESULT_REASONS[task.result_reason] || task.result_reason}
                </span>
            );
        }
        return null;
    }
    const attention = waitNeedsAttention(task.waiting_on);
    const colour = attention ? 'bg-amber-100 text-amber-800' : 'bg-indigo-50 text-indigo-700';
    const wait = task.waiting_on ? waitLabel(task.waiting_on) : '';
    return (
        <span
            data-testid="phase-chip"
            title={task.wait_detail || undefined}
            className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ${colour} ${className || ''}`}
        >
            {task.phase && <span>{phaseLabel(task.phase)}</span>}
            {task.phase && wait && <span aria-hidden="true">·</span>}
            {wait && <span className="font-normal">{wait}</span>}
        </span>
    );
};

export const TaskTypeBadge: React.FC<{ type: string }> = ({ type }) => (
    <span className="inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 text-[11px] font-medium uppercase tracking-wide text-gray-600">
        {taskTypeLabel(type)}
    </span>
);
