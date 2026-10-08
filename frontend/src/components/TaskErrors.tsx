import React, { useState } from 'react';
import { Link } from 'react-router-dom';
import { AlertTriangle } from 'lucide-react';
import { CallLog } from './CallLog';
import { formatDateTime } from '../lib/workflow';
import type { TaskErrorReport } from '../lib/workflow';

const KIND_LABELS: Record<string, string> = {
    model_call: 'A model could not be called',
    rejected_answer: 'A model answered with something that could not be used',
    session: 'An executor session ended without reporting',
};

// TaskErrors lists what went wrong beneath a task that was not the work's own
// outcome: models that could not be called, answers that could not be used,
// sessions that crashed. The same error is shown once, with how often it came
// and where, so fifty subtasks failing alike read as one problem.
export const TaskErrors: React.FC<{ report: TaskErrorReport | null; companyPath: string }> = ({ report, companyPath }) => {
    const [openCall, setOpenCall] = useState<number | null>(null);
    if (!report) return <p className="text-sm text-gray-500">Loading…</p>;
    if (report.groups.length === 0) {
        return <p className="text-sm italic text-gray-500" data-testid="task-errors-empty">Nothing went wrong in this task or beneath it.</p>;
    }
    return (
        <div className="space-y-3" data-testid="task-errors-list">
            <p className="text-xs text-gray-500">
                Problems met while working on this task and its subtasks, the most recent first. A subtask reporting that it could not do its work is a result and is shown with the subtask, not here.
            </p>
            {report.groups.map((group, index) => (
                <div key={index} className="rounded-lg border border-red-200 bg-red-50/60 p-3" data-testid="task-error">
                    <div className="flex items-start gap-2">
                        <AlertTriangle size={16} className="mt-0.5 shrink-0 text-red-600" />
                        <div className="min-w-0 flex-1">
                            <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
                                <span className="font-medium text-red-900">{KIND_LABELS[group.kind] || group.kind}</span>
                                {group.count > 1 && (
                                    <span className="rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-800" data-testid="task-error-count">{group.count} times</span>
                                )}
                            </div>
                            <pre className="mt-1.5 whitespace-pre-wrap break-words rounded border border-red-100 bg-white p-2 text-xs text-gray-800" data-testid="task-error-message">{group.message}</pre>
                            <p className="mt-1.5 text-xs text-gray-600">
                                {[group.provider, group.model, group.tier && `${group.tier} tier`].filter(Boolean).join(' · ')}
                                {(group.provider || group.model) && ' · '}
                                {group.count > 1 ? `${formatDateTime(group.first_at)} to ${formatDateTime(group.last_at)}` : formatDateTime(group.last_at)}
                            </p>
                            {group.tasks.length > 0 && (
                                <p className="mt-1 flex flex-wrap gap-x-2 gap-y-0.5 text-xs text-gray-600">
                                    <span>In:</span>
                                    {group.tasks.slice(0, 8).map(task => (
                                        <Link key={task.id} to={`${companyPath}/tasks/${task.id}`} title={task.title} className="font-mono text-indigo-700 hover:underline">
                                            {task.ref_key || `#${task.id}`}
                                        </Link>
                                    ))}
                                    {group.tasks.length > 8 && <span>and {group.tasks.length - 8} more</span>}
                                </p>
                            )}
                        </div>
                        {group.call_id ? (
                            <button type="button" onClick={() => setOpenCall(group.call_id!)} className="shrink-0 rounded border border-red-200 bg-white px-2 py-1 text-xs text-red-800 hover:bg-red-50">
                                Open the call
                            </button>
                        ) : null}
                    </div>
                </div>
            ))}
            {openCall !== null && <CallLog key={openCall} callId={openCall} onClose={() => setOpenCall(null)} />}
        </div>
    );
};
