import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import axios from 'axios';
import { PhaseChip } from './PhaseChip';
import { TaskTree } from './TaskTree';
import { TaskJournal } from './TaskJournal';
import { UsagePanel } from './UsagePanel';
import { RunTree } from './RunTree';
import type { RunTask } from './RunTree';
import { TaskErrors } from './TaskErrors';
import { ClassifierNotice } from './ClassifierNotice';
import type { Decision, Task, TaskStep } from '../lib/workflow';

vi.mock('axios', () => ({ default: { get: vi.fn() } }));

const task = (fields: Partial<Task>): Task => ({
    id: 1, company_id: 1, root_task_id: 1, depth: 0, title: 'Task', status: 'in-progress', task_type: 'coding',
    mode: 'managed', phase: '', waiting_on: '', wait_detail: '', ...fields,
});

afterEach(cleanup);
beforeEach(() => vi.clearAllMocks());

describe('PhaseChip', () => {
    it('shows the step a working task is in and what it waits for', () => {
        render(<PhaseChip task={task({ phase: 'execute', waiting_on: 'subtasks' })} />);
        expect(screen.getByTestId('phase-chip').textContent).toBe('Execute·waiting for subtasks');
    });

    it('stands out when the task needs a person', () => {
        render(<PhaseChip task={task({ status: 'blocked', phase: 'refine', waiting_on: 'human', wait_detail: 'needs your direction' })} />);
        const chip = screen.getByTestId('phase-chip');
        expect(chip.textContent).toContain('waiting for your answer');
        expect(chip.className).toContain('amber');
        expect(chip.getAttribute('title')).toBe('needs your direction');
    });

    it('says why a task failed, and stays quiet for one that is at rest', () => {
        const { rerender } = render(<PhaseChip task={task({ status: 'failed', result_reason: 'cannot_complete' })} />);
        expect(screen.getByTestId('phase-chip').textContent).toContain('could not be completed');
        rerender(<PhaseChip task={task({ status: 'done' })} />);
        expect(screen.queryByTestId('phase-chip')).toBeNull();
        rerender(<PhaseChip task={task({ status: 'backlog' })} />);
        expect(screen.queryByTestId('phase-chip')).toBeNull();
    });
});

describe('TaskTree', () => {
    const root = task({ id: 1, title: 'Ship login', ref_key: 'A-1' });
    const implement = task({ id: 2, parent_id: 1, depth: 1, title: 'Implement', ref_key: 'A-1-1', mode: 'direct', status: 'done', result_summary: 'Added the handler.', origin_phase: 'plan' });
    const review = task({
        id: 3, parent_id: 1, depth: 1, title: 'Review', ref_key: 'A-1-2', task_type: 'review', mode: 'direct',
        status: 'depends-on-task', relation_summary: { depends_on: [{ id: 2 }] },
    });
    const question = task({ id: 4, parent_id: 2, depth: 2, title: 'Which hash?', ref_key: 'A-1-1-1', task_type: 'research', status: 'failed', result_reason: 'cannot_complete' });

    const renderTree = (decisions?: Decision[]) => render(
        <MemoryRouter><TaskTree tasks={[root, implement, review, question]} companyPath="/companies/acme" decisions={decisions} /></MemoryRouter>,
    );

    it('nests subtasks the way they were delegated, with their state and what they wait for', () => {
        renderTree();
        const tree = screen.getByTestId('subtask-tree');
        const rows = within(tree).getAllByTestId('tree-task');
        // The root is the frame; its three descendants are the rows with content.
        expect(within(tree).getByText('A-1-1 Implement')).toBeTruthy();
        expect(within(tree).getByText('Added the handler.')).toBeTruthy();
        expect(within(tree).getByTestId('tree-task-after').textContent).toBe('after A-1-1');
        expect(within(tree).getByText('it could not be completed with what is available')).toBeTruthy();
        // The question sits inside the task that asked it.
        const implementRow = rows.find(row => row.textContent?.startsWith('A-1-1 Implement'))!;
        expect(within(implementRow).getByText('A-1-1-1 Which hash?')).toBeTruthy();
        expect(within(tree).getByText('A-1-1 Implement').getAttribute('href')).toBe('/companies/acme/tasks/2');
    });

    it('says so when a task has delegated nothing', () => {
        render(<MemoryRouter><TaskTree tasks={[root]} companyPath="/companies/acme" /></MemoryRouter>);
        expect(screen.getByText('No subtasks yet.')).toBeTruthy();
    });

    it('becomes the decision tree: each record under the task that made it, revised ones struck out', () => {
        const decision = (fields: Partial<Decision>): Decision => ({
            id: 1, task_id: 1, phase: 'plan', kind: 'decision', title: 't', decision: '', rationale: '', alternatives: '', created_at: '', ...fields,
        });
        renderTree([
            decision({ id: 10, task_id: 1, title: 'Use sessions, not JWT', rationale: 'Simpler to revoke.', alternatives: 'JWT' }),
            decision({ id: 11, task_id: 4, kind: 'dead_end', title: 'bcrypt is unavailable', phase: 'execute' }),
            decision({ id: 12, task_id: 4, kind: 'assumption', title: 'argon2 is acceptable', supersedes_id: 11 }),
        ]);
        const tree = screen.getByTestId('decision-tree');
        const records = within(tree).getAllByTestId('decision');
        expect(records).toHaveLength(3);
        expect(within(tree).getByText('Simpler to revoke.')).toBeTruthy();
        expect(records[1].textContent).toContain('Dead end');
        expect(records[1].textContent).toContain('Later revised.');
        expect(records[2].textContent).toContain('Assumed');
        // Tasks that decided nothing are left out; the path to those that did stays.
        expect(within(tree).queryByText('A-1-2 Review')).toBeNull();
        expect(within(tree).getByText('A-1-1 Implement')).toBeTruthy();
    });
});

describe('TaskJournal', () => {
    const step = (fields: Partial<TaskStep>): TaskStep => ({
        id: 1, task_id: 7, kind: 'note', phase: '', tool_name: '', tool_args: '', prompt: '', response: '', result: '', error: '', created_at: '2026-10-08T10:00:00Z', ...fields,
    });

    it('lists what happened and opens a smart call to its full prompt and answer', async () => {
        vi.mocked(axios.get).mockResolvedValue({ data: step({
            id: 2, kind: 'smart_call',
            prompt: JSON.stringify({ messages: [{ role: 'user', content: 'What must be done?' }] }),
            response: JSON.stringify({ choices: [{ message: { tool_calls: [{ function: { name: 'ask_questions', arguments: '{"questions":[]}' } }] } }] }),
        }) } as never);
        render(
            <MemoryRouter>
                <TaskJournal taskId={7} companyPath="/companies/acme" steps={[
                    step({ id: 1, kind: 'phase_entered', phase: 'refine' }),
                    step({ id: 2, kind: 'smart_call', phase: 'refine', tool_name: 'ask_questions', result: '2 questions' }),
                    step({ id: 3, kind: 'run_started', run_id: 55, result: 'attempt 1' }),
                    step({ id: 4, kind: 'subtask_finished', ref_task_id: 9, result: 'done' }),
                ]} />
            </MemoryRouter>,
        );

        expect(screen.getByTestId('journal-step-phase_entered').textContent).toContain('Refine');
        expect(screen.getByText('session #55').getAttribute('href')).toBe('/companies/acme/run-logs/55');
        expect(screen.getByText('task #9').getAttribute('href')).toBe('/companies/acme/tasks/9');
        // The prompt is not loaded until the step is opened.
        expect(axios.get).not.toHaveBeenCalled();

        fireEvent.click(screen.getByText('ask_questions — 2 questions'));
        await waitFor(() => expect(axios.get).toHaveBeenCalledWith('/api/tasks/7/steps/2'));
        expect((await screen.findByTestId('smart-prompt')).textContent).toBe('What must be done?');
        expect(screen.getByTestId('smart-answer').textContent).toContain('"questions"');
    });

    it('says so when nothing has happened', () => {
        render(<MemoryRouter><TaskJournal taskId={7} companyPath="/c" steps={[]} /></MemoryRouter>);
        expect(screen.getByText('Nothing has happened on this task yet.')).toBeTruthy();
    });
});

describe('UsagePanel', () => {
    const totals = { calls: 6, failed_calls: 1, prompt_tokens: 390, completion_tokens: 39, reasoning_tokens: 0, cached_tokens: 0, duration_ms: 65000 };
    const group = (key: string, label: string, prompt: number, calls = 1) => ({ ...totals, key, label, calls, failed_calls: 0, prompt_tokens: prompt, completion_tokens: 0 });
    const report = {
        totals,
        groups: {
            phase: [group('plan', 'plan', 200, 2), group('refine', 'refine', 190, 4)],
            phase_tier: [group('refine/smart', '', 100), group('refine/cheap', '', 90), group('plan/smart', '', 200)],
            task: [group('10', 'A-1 Ship login', 300, 3), group('12', 'A-1-1 Look it up', 90, 3)],
        },
    };
    const call = { id: 77, company_id: 3, agent_name: 'Coder', tier: 'cheap', purpose: 'executor_turn', phase: 'execute', workflow_phase: 'refine', provider_name: 'p', model: 'small', requested_model: 'small', run_id: 5, log_seq: 4, prompt_tokens: 30, completion_tokens: 3, reasoning_tokens: 0, cached_tokens: 0, duration_ms: 900, status: 'ok', error: '', created_at: '2026-10-01T12:00:00Z' };

    beforeEach(() => {
        vi.mocked(axios.get).mockImplementation(async (url: string) => {
            if (url === '/api/tasks/10/usage') return { data: report } as never;
            if (url.startsWith('/api/usage/calls?')) return { data: { calls: [call] } } as never;
            if (url === '/api/usage/calls/77') return { data: { call, entries: [{ seq: 4, type: 'info', content: 'second answer' }] } } as never;
            throw new Error(`unexpected request ${url}`);
        });
    });

    const renderPanel = () => render(
        <MemoryRouter>
            <UsagePanel reportUrl="/api/tasks/10/usage" callsQuery="company_id=3&task_id=10&subtree=true" dimensions={['phase', 'task']} />
        </MemoryRouter>,
    );

    it('shows totals and each breakdown, steps in workflow order with smart and cheap spend apart', async () => {
        renderPanel();
        expect((await screen.findByTestId('usage-total-tokens')).textContent).toBe('429');
        expect(screen.getByText('6 (1 failed)')).toBeTruthy();

        const steps = within(screen.getByTestId('usage-by-phase')).getAllByTestId('usage-row');
        expect(steps.map(row => row.textContent?.replace(/smart.*/, '').trim())).toEqual(['Refine', 'Plan']);
        expect(steps[0].textContent).toContain('smart 100 · cheap 90');
        expect(within(screen.getByTestId('usage-by-task')).getAllByTestId('usage-row-tokens').map(cell => cell.textContent)).toEqual(['300', '90']);
    });

    it('expands a row to its calls and opens a call as a log', async () => {
        renderPanel();
        const tasks = await screen.findByTestId('usage-by-task');
        fireEvent.click(within(tasks).getByText('A-1-1 Look it up'));

        // The row narrows the ledger to that one subtask, not the whole tree.
        await waitFor(() => expect(axios.get).toHaveBeenCalledWith('/api/usage/calls?company_id=3&task_id=12&limit=50'));
        fireEvent.click(await within(tasks).findByTestId('usage-call'));

        const log = await screen.findByTestId('call-log');
        await waitFor(() => expect(axios.get).toHaveBeenCalledWith('/api/usage/calls/77'));
        expect(log.textContent).toContain('Executor call · small');
        expect(await within(log).findByText('second answer')).toBeTruthy();
    });
});

describe('RunTree', () => {
    const runTask = (fields: Partial<RunTask>): RunTask => ({
        id: 1, root_task_id: 18, ref_key: '', title: '', task_type: 'research', status: 'failed', phase: '', waiting_on: '', wait_detail: '', ...fields,
    });
    const data = {
        tasks: [
            runTask({ id: 19, parent_id: 18, ref_key: 'GL-18-1', title: 'What is the project name?' }),
            runTask({ id: 18, ref_key: 'GL-18', title: 'Research current project', task_type: 'general', status: 'blocked', phase: 'refine', waiting_on: 'human' }),
            runTask({ id: 20, parent_id: 18, ref_key: 'GL-18-2', title: 'Which stack is used?', status: 'done' }),
            runTask({ id: 7, root_task_id: 7, ref_key: 'GL-7', title: 'An older task', status: 'done' }),
        ],
        runs: [
            { id: 68, task_id: 19, name: 'GL-18-1-CEO-1', status: 'failed', started_at: '2026-10-08T19:49:53Z', ended_at: '2026-10-08T19:49:55Z', agent_name: 'CEO' },
            { id: 74, task_id: 19, name: 'GL-18-1-CEO-2', status: 'failed', attempt: 2, started_at: '2026-10-08T19:49:56Z', ended_at: '2026-10-08T19:49:58Z', agent_name: 'CEO' },
            { id: 69, task_id: 20, name: 'GL-18-2-CEO-1', status: 'completed', started_at: '2026-10-08T19:49:53Z', ended_at: '2026-10-08T19:50:53Z', agent_name: 'CEO' },
            { id: 3, task_id: 7, name: 'GL-7-CODER-1', status: 'completed', started_at: '2026-10-01T10:00:00Z', ended_at: '2026-10-01T10:05:00Z', agent_name: 'Coder' },
        ],
    };
    const renderTree = () => render(<MemoryRouter><RunTree data={data} companyPath="/companies/gl" /></MemoryRouter>);

    it('puts sessions under their task and tasks under their parent, the latest tree first', () => {
        renderTree();
        const rows = screen.getAllByTestId('run-tree-task');
        expect(rows.map(row => row.getAttribute('data-depth'))).toEqual(['0', '1', '1', '0']);
        expect(rows[0].textContent).toContain('GL-18');
        expect(rows[0].textContent).toContain('Research current project');
        expect(rows[3].textContent).toContain('GL-7');
        // The top-level task ran no session itself; its line counts what ran beneath it.
        expect(within(rows[0]).getAllByTestId('run-tree-tally')[0].textContent).toBe('3 sessions · 2 failed');
        expect(within(rows[0]).getByTestId('phase-chip').textContent).toContain('waiting for your answer');
    });

    it('shows a small tree open to its sessions, each a link to its log', () => {
        renderTree();
        expect(screen.getByText('GL-18-1-CEO-1').closest('a')?.getAttribute('href')).toBe('/companies/gl/run-logs/68');
        expect(screen.getByText('attempt 2')).toBeTruthy();
        fireEvent.click(screen.getByLabelText('Collapse GL-18-1'));
        expect(screen.queryByText('GL-18-1-CEO-1')).toBeNull();
        fireEvent.click(screen.getByLabelText('Collapse GL-18'));
        expect(screen.queryByText('GL-18-1')).toBeNull();
    });

    it('keeps the subtasks of a large tree closed until asked, with their tallies in view', () => {
        const tasks = [runTask({ id: 18, ref_key: 'GL-18', title: 'Research current project', status: 'blocked' })];
        const runs = [];
        for (let i = 1; i <= 20; i++) {
            tasks.push(runTask({ id: 100 + i, parent_id: 18, ref_key: `GL-18-${i}`, title: `Question ${i}` }));
            runs.push({ id: 200 + i, task_id: 100 + i, name: `GL-18-${i}-CEO-1`, status: 'failed' });
        }
        render(<MemoryRouter><RunTree data={{ tasks, runs }} companyPath="/companies/gl" /></MemoryRouter>);
        expect(screen.getAllByTestId('run-tree-task')).toHaveLength(21);
        expect(screen.queryAllByTestId('run-card')).toHaveLength(0);
        expect(screen.getAllByTestId('run-tree-tally')[0].textContent).toBe('20 sessions · 20 failed');
        fireEvent.click(screen.getByLabelText('Expand GL-18-3'));
        expect(screen.getByText('GL-18-3-CEO-1')).toBeTruthy();
    });

    it('says so when nothing has run', () => {
        render(<MemoryRouter><RunTree data={{ runs: [], tasks: [] }} companyPath="/companies/gl" emptyText="Nothing yet." /></MemoryRouter>);
        expect(screen.getByText('Nothing yet.')).toBeTruthy();
    });
});

describe('TaskTree sessions', () => {
    it('lists each subtask\'s sessions under it', () => {
        const root = task({ id: 1, title: 'Ship login', ref_key: 'A-1' });
        const child = task({ id: 2, parent_id: 1, depth: 1, title: 'Implement', ref_key: 'A-1-1', mode: 'direct', status: 'failed' });
        render(
            <MemoryRouter>
                <TaskTree tasks={[root, child]} companyPath="/companies/acme" runs={[
                    { id: 9, task_id: 2, name: 'A-1-1-CODER-1', status: 'failed' },
                    { id: 10, task_id: 2, name: 'A-1-1-CODER-2', status: 'completed' },
                ]} />
            </MemoryRouter>,
        );
        const sessions = screen.getByTestId('tree-task-sessions');
        expect(sessions.textContent).toBe('Sessions:A-1-1-CODER-1 (failed)A-1-1-CODER-2');
        expect(within(sessions).getByText('A-1-1-CODER-1 (failed)').getAttribute('href')).toBe('/companies/acme/run-logs/9');
    });
});

describe('TaskErrors', () => {
    it('shows one error once, with how often it came and where', () => {
        render(
            <MemoryRouter>
                <TaskErrors companyPath="/companies/gl" report={{ total: 54, groups: [{
                    kind: 'model_call', message: 'Request is missing x-opencode-session', count: 54,
                    first_at: '2026-10-08T19:49:53Z', last_at: '2026-10-08T19:54:26Z',
                    provider: 'Deepseek V4 Flash', model: 'deepseek-v4-flash', tier: 'cheap', call_id: 501,
                    tasks: [{ id: 19, ref_key: 'GL-18-1', title: 'What is the project name?' }, { id: 20, ref_key: 'GL-18-2', title: 'Which stack?' }],
                }] }} />
            </MemoryRouter>,
        );
        expect(screen.getAllByTestId('task-error')).toHaveLength(1);
        expect(screen.getByTestId('task-error-count').textContent).toBe('54 times');
        expect(screen.getByTestId('task-error-message').textContent).toBe('Request is missing x-opencode-session');
        expect(screen.getByText('A model could not be called')).toBeTruthy();
        expect(screen.getByText('GL-18-1').getAttribute('href')).toBe('/companies/gl/tasks/19');
        expect(screen.getByTestId('task-error').textContent).toContain('Deepseek V4 Flash · deepseek-v4-flash · cheap tier');
    });

    it('says so when nothing went wrong', () => {
        render(<MemoryRouter><TaskErrors companyPath="/companies/gl" report={{ total: 0, groups: [] }} /></MemoryRouter>);
        expect(screen.getByTestId('task-errors-empty')).toBeTruthy();
    });
});

describe('ClassifierNotice', () => {
    it('warns when no classifier is set and offers what it was given to fix it', async () => {
        vi.mocked(axios.get).mockResolvedValue({ data: [{ purpose: 'smart', provider_id: 1 }, { purpose: 'classifier', provider_id: null }] } as never);
        render(<ClassifierNotice><button>Connect TypeSafe (Jev)</button></ClassifierNotice>);
        const warning = await screen.findByTestId('classifier-warning');
        expect(warning.textContent).toContain('No classifier is set');
        expect(warning.textContent).toContain('Tasks still run, on fixed rules');
        expect(within(warning).getByText('Connect TypeSafe (Jev)')).toBeTruthy();
    });

    it('is silent once one is chosen, a model or a group of them', async () => {
        for (const chosen of [{ provider_id: 4 }, { provider_id: null, model_group_id: 9 }]) {
            vi.mocked(axios.get).mockResolvedValue({ data: [{ purpose: 'classifier', ...chosen }] } as never);
            const { unmount } = render(<ClassifierNotice />);
            await waitFor(() => expect(axios.get).toHaveBeenCalledWith('/api/default-model-settings'));
            expect(screen.queryByTestId('classifier-warning')).toBeNull();
            unmount();
            vi.clearAllMocks();
        }
    });
});
