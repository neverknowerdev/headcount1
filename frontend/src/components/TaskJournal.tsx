import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { Link } from 'react-router-dom';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { SmartExchange } from './CallLog';
import { STEP_LABELS, errorMessage, formatDateTime, phaseLabel } from '../lib/workflow';
import type { TaskStep } from '../lib/workflow';

const STEP_COLOURS: Record<string, string> = {
    smart_call: 'bg-indigo-100 text-indigo-700',
    smart_error: 'bg-red-100 text-red-700',
    human_question: 'bg-amber-100 text-amber-800',
    human_answer: 'bg-amber-100 text-amber-800',
    run_started: 'bg-sky-100 text-sky-700',
    run_finished: 'bg-sky-100 text-sky-700',
    checkpoint: 'bg-sky-50 text-sky-700',
    finished: 'bg-green-100 text-green-700',
    stopped: 'bg-gray-200 text-gray-700',
};

// summarize says in one line what a step did.
function summarize(step: TaskStep): string {
    if (step.kind === 'phase_entered' || step.kind === 'workflow_started') return phaseLabel(step.phase);
    if (step.kind === 'smart_call' && step.tool_name) return step.result ? `${step.tool_name} — ${step.result}` : step.tool_name;
    return step.result || step.error || '';
}

// SmartStep fetches a smart call's full prompt and answer when it is opened;
// the journal list leaves them out because they are large.
const SmartStep: React.FC<{ taskId: number; stepId: number }> = ({ taskId, stepId }) => {
    const [step, setStep] = useState<TaskStep | null>(null);
    const [error, setError] = useState<string | null>(null);
    useEffect(() => {
        let current = true;
        axios.get(`/api/tasks/${taskId}/steps/${stepId}`)
            .then(res => { if (current) setStep(res.data); })
            .catch(e => { if (current) setError(errorMessage(e, 'Could not load the step')); });
        return () => { current = false; };
    }, [taskId, stepId]);
    if (error) return <p className="text-xs text-red-600">{error}</p>;
    if (!step) return <p className="text-xs text-gray-400">Loading…</p>;
    return <SmartExchange prompt={step.prompt} response={step.response} error={step.error} />;
};

interface Props {
    taskId: number;
    steps: TaskStep[];
    // Base path of the company's pages, for links to executor sessions and subtasks.
    companyPath: string;
}

// TaskJournal is a task's own record, oldest first: each phase it entered,
// each question put to the smart model with its answer, each subtask it
// delegated, each executor session and each exchange with the human.
export const TaskJournal: React.FC<Props> = ({ taskId, steps, companyPath }) => {
    const [open, setOpen] = useState<number | null>(null);
    if (steps.length === 0) return <p className="text-sm italic text-gray-400">Nothing has happened on this task yet.</p>;
    return (
        <ol className="space-y-1" data-testid="task-journal">
            {steps.map(step => {
                const expandable = step.kind === 'smart_call' || step.kind === 'smart_error';
                const isOpen = open === step.id;
                const summary = summarize(step);
                return (
                    <li key={step.id} data-testid={`journal-step-${step.kind}`} className="rounded border border-gray-100 bg-white">
                        <div
                            className={`flex items-start gap-2 px-3 py-2 text-sm ${expandable ? 'cursor-pointer hover:bg-gray-50' : ''}`}
                            onClick={() => expandable && setOpen(isOpen ? null : step.id)}
                        >
                            <span className="mt-0.5 w-4 flex-shrink-0 text-gray-400">
                                {expandable && (isOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />)}
                            </span>
                            <span className={`flex-shrink-0 rounded px-1.5 py-0.5 text-[11px] font-medium ${STEP_COLOURS[step.kind] || 'bg-gray-100 text-gray-600'}`}>
                                {STEP_LABELS[step.kind] || step.kind}
                            </span>
                            <span className="min-w-0 flex-1 whitespace-pre-wrap break-words text-gray-800">{summary}</span>
                            {step.run_id && (
                                <Link to={`${companyPath}/run-logs/${step.run_id}`} onClick={e => e.stopPropagation()} className="flex-shrink-0 text-xs text-indigo-600 hover:underline">
                                    session #{step.run_id}
                                </Link>
                            )}
                            {step.ref_task_id && (
                                <Link to={`${companyPath}/tasks/${step.ref_task_id}`} onClick={e => e.stopPropagation()} className="flex-shrink-0 text-xs text-indigo-600 hover:underline">
                                    task #{step.ref_task_id}
                                </Link>
                            )}
                            <span className="flex-shrink-0 text-[11px] text-gray-400">{formatDateTime(step.created_at)}</span>
                        </div>
                        {expandable && isOpen && (
                            <div className="border-t border-gray-100 bg-gray-50/50 p-3">
                                <SmartStep taskId={taskId} stepId={step.id} />
                            </div>
                        )}
                    </li>
                );
            })}
        </ol>
    );
};
