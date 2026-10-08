// Shapes and labels of the task workflow, as the API serves them.

export interface Task {
    id: number;
    company_id: number;
    project_id?: number | null;
    sprint_id?: number;
    agent_id?: number | null;
    parent_id?: number | null;
    root_task_id: number;
    depth: number;
    title: string;
    description?: string;
    ref_key?: string;
    status: string;
    task_type: string;
    mode: string;
    phase: string;
    waiting_on: string;
    wait_detail: string;
    origin_phase?: string;
    provider_id?: number | null;
    model_group_id?: number | null;
    model?: string;
    result_reason?: string;
    result_summary?: string;
    result_details?: string;
    result_verdict?: string;
    relation_summary?: { depends_on?: { id: number }[] } | null;
    [key: string]: unknown;
}

export interface TaskStep {
    id: number;
    task_id: number;
    kind: string;
    phase: string;
    agent_id?: number | null;
    run_id?: number | null;
    ref_task_id?: number | null;
    comment_id?: number | null;
    tool_name: string;
    tool_args: string;
    prompt: string;
    response: string;
    result: string;
    error: string;
    created_at: string;
}

export interface Decision {
    id: number;
    task_id: number;
    parent_decision_id?: number | null;
    step_id?: number | null;
    run_id?: number | null;
    phase: string;
    kind: string;
    title: string;
    decision: string;
    rationale: string;
    alternatives: string;
    supersedes_id?: number | null;
    created_at: string;
}

export interface UsageTotals {
    calls: number;
    failed_calls: number;
    prompt_tokens: number;
    completion_tokens: number;
    reasoning_tokens: number;
    cached_tokens: number;
    duration_ms: number;
}

export interface UsageGroup extends UsageTotals {
    key: string;
    label: string;
}

export interface UsageReport {
    totals: UsageTotals;
    groups: Record<string, UsageGroup[]>;
}

export interface LLMCall {
    id: number;
    company_id: number;
    task_id?: number | null;
    root_task_id?: number | null;
    agent_id?: number | null;
    agent_name: string;
    tier: string;
    purpose: string;
    phase: string;
    workflow_phase: string;
    provider_name: string;
    model: string;
    requested_model: string;
    step_id?: number | null;
    run_id?: number | null;
    log_seq?: number | null;
    prompt_tokens: number;
    completion_tokens: number;
    reasoning_tokens: number;
    cached_tokens: number;
    duration_ms: number;
    status: string;
    error: string;
    created_at: string;
}

export const TASK_TYPES: { value: string; label: string; hint: string }[] = [
    { value: 'general', label: 'General', hint: 'Anything that is not mainly research, code or a review.' },
    { value: 'research', label: 'Research', hint: 'Find something out and report it. Nothing is changed.' },
    { value: 'coding', label: 'Coding', hint: 'Change code: designed by the CTO, test-planned by the QA Lead, implemented and reviewed.' },
    { value: 'review', label: 'Review', hint: 'Judge existing work and say what must change.' },
];

export const taskTypeLabel = (type: string): string => TASK_TYPES.find(t => t.value === type)?.label || type;

// The workflow steps in the order a task goes through them. Design and
// test plan exist only for coding tasks; adjust only when a plan had to change.
export const PHASES = ['refine', 'design', 'test_plan', 'plan', 'execute', 'adjust', 'verify'];

export const PHASE_LABELS: Record<string, string> = {
    refine: 'Refine',
    design: 'Design',
    test_plan: 'Test plan',
    plan: 'Plan',
    execute: 'Execute',
    adjust: 'Adjust',
    verify: 'Verify',
    '': 'Outside a task',
};

export const phaseLabel = (phase: string): string => PHASE_LABELS[phase] ?? phase;

const WAIT_LABELS: Record<string, string> = {
    subtasks: 'waiting for subtasks',
    run: 'working',
    human: 'waiting for your answer',
    vault: 'vault locked',
    config: 'no model configured',
    workspace: 'waiting for the worktree',
    backoff: 'retrying shortly',
    operator: 'stopped',
};

export const waitLabel = (waitingOn: string): string => WAIT_LABELS[waitingOn] ?? waitingOn;

// Waits a person has to do something about.
export const waitNeedsAttention = (waitingOn: string): boolean =>
    waitingOn === 'human' || waitingOn === 'vault' || waitingOn === 'config' || waitingOn === 'operator';

export const STATUS_LABELS: Record<string, string> = {
    backlog: 'Backlog',
    'to-do': 'To do',
    'in-progress': 'In progress',
    blocked: 'Blocked',
    'depends-on-task': 'Waiting for tasks',
    'in-review': 'In review',
    done: 'Done',
    failed: 'Failed',
    canceled: 'Canceled',
};

export const statusLabel = (status: string): string => STATUS_LABELS[status] ?? status;

// A task the workflow is working on or about to: it cannot be moved by hand.
export const isTaskRunning = (status: string): boolean =>
    status === 'to-do' || status === 'depends-on-task' || status === 'in-progress';

// A task that can be stopped: running, or blocked on something other than a stop.
export const canStopTask = (task: Pick<Task, 'status' | 'waiting_on'>): boolean =>
    isTaskRunning(task.status) || (task.status === 'blocked' && task.waiting_on !== 'operator');

export const RESULT_REASONS: Record<string, string> = {
    reported_failure: 'the executor reported a failure',
    cannot_complete: 'it could not be completed with what is available',
    run_error: 'the executor session crashed',
    budget_exhausted: 'it ran out of attempts',
    prerequisite_failed: 'a task it depends on did not succeed',
    stopped: 'it was stopped',
};

export const STEP_LABELS: Record<string, string> = {
    workflow_started: 'Started',
    phase_entered: 'Phase',
    smart_call: 'Decision',
    smart_error: 'Model call failed',
    subtask_finished: 'Subtask finished',
    human_question: 'Asked you',
    human_answer: 'You answered',
    run_started: 'Executor started',
    run_finished: 'Executor finished',
    checkpoint: 'Checkpoint',
    waiting: 'Waiting',
    resumed: 'Resumed',
    review_round: 'Review round',
    stopped: 'Stopped',
    rerun: 'Run again',
    finished: 'Finished',
    note: 'Note',
};

export const formatTokens = (n: number): string => {
    if (!n || n < 1000) return String(n || 0);
    if (n < 10000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'K';
    if (n < 1000000) return Math.round(n / 1000) + 'K';
    return (n / 1000000).toFixed(1).replace(/\.0$/, '') + 'M';
};

export const formatDuration = (ms: number): string => {
    if (!ms || ms < 0) return '0s';
    if (ms < 1000) return `${ms}ms`;
    const sec = Math.round(ms / 1000);
    if (sec < 60) return `${sec}s`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ${sec % 60}s`;
    return `${Math.floor(min / 60)}h ${min % 60}m`;
};

export const formatDateTime = (value?: string | null): string => {
    if (!value) return '—';
    const d = new Date(value);
    return d.getFullYear() > 1 ? d.toLocaleString() : '—';
};

// errorMessage reads the message of a failed request: the API's own error
// text when it sent one.
export const errorMessage = (e: unknown, fallback: string): string => {
    const failure = e as { response?: { data?: { error?: string } }; message?: string } | null;
    return failure?.response?.data?.error || failure?.message || fallback;
};

// rowQuery narrows a set of usage calls to one row of a breakdown. A task row
// replaces the task the panel itself is about: one task exactly, or for a
// top-level task everything beneath it.
export function rowQuery(baseQuery: string, dimension: string, param: string, key: string): string {
    const query = new URLSearchParams(baseQuery);
    query.set(param, key);
    if (dimension === 'task') query.delete('subtree');
    if (dimension === 'root_task') query.set('subtree', 'true');
    return query.toString();
}

export interface SmartExchangeView {
    system: string;
    user: string;
    tools: string[];
    answerTool: string;
    answerArguments: string;
    answerText: string;
}

const prettyJSON = (raw: string): string => {
    try {
        return JSON.stringify(JSON.parse(raw), null, 2);
    } catch {
        return raw;
    }
};

// A smart call is one request and one answer. The journal stores both as the
// provider saw them; this pulls out the parts a person reads.
export function readSmartExchange(prompt: string, response: string): SmartExchangeView {
    const view: SmartExchangeView = { system: '', user: '', tools: [], answerTool: '', answerArguments: '', answerText: '' };
    try {
        const request = JSON.parse(prompt || '{}') as {
            messages?: { role?: string; content?: unknown }[];
            tools?: { function?: { name?: string } }[];
        };
        for (const message of request.messages || []) {
            if (message.role === 'system') view.system = String(message.content || '');
            if (message.role === 'user') view.user = String(message.content || '');
        }
        view.tools = (request.tools || []).map(tool => tool?.function?.name || '').filter(Boolean);
    } catch {
        view.user = prompt;
    }
    try {
        const reply = JSON.parse(response || '{}') as {
            choices?: { message?: { content?: unknown; tool_calls?: { function?: { name?: string; arguments?: string } }[] } }[];
        };
        const message = reply.choices?.[0]?.message;
        const call = message?.tool_calls?.[0]?.function;
        if (call) {
            view.answerTool = call.name || '';
            view.answerArguments = prettyJSON(call.arguments || '');
        }
        view.answerText = typeof message?.content === 'string' ? message.content : '';
    } catch {
        view.answerText = response;
    }
    return view;
}
