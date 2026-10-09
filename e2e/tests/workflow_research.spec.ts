import { test, expect } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';
import { resetE2E } from '../helpers/reset';
import { waitForTaskStatus } from '../helpers/wait-for';
import {
    CHEAP_MODEL, SMART_MODEL, call, createTask, createWorkspace, decided, finishWork, getDecisions, getJSON, getSteps,
    getTree, getUsage, mockLog, promptText, releaseMock, setScenario, startTask,
} from '../helpers/workflow';

const env = loadE2EEnv();

// A research task from start to review. The smart model asks two questions
// before it can refine; each becomes a research subtask on the cheap model and
// the two run side by side. One cannot be answered, and says why. The smart
// model decides with what it has, plans, and verifies the result.
test.describe('Workflow: research task', () => {
    test.beforeEach(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test('questions run in parallel, an unanswerable one comes back with its reason, and the smart model decides', async ({ request, page }) => {
        const workspace = await createWorkspace(request, 'Research Co', 'research-co');
        const task = await createTask(request, workspace, {
            title: 'Compare the two billing providers',
            description: 'Say which billing provider we should move to and why.',
            task_type: 'research',
        });

        await setScenario([
            // Refinement, first step: two things must be known first.
            { match: { phase: 'refine' }, replies: [
                call('ask_questions', {
                    questions: [
                        { question: 'What does Stripely charge per transaction?', context: 'Public pricing page.' },
                        { question: 'What does our current contract with Payzo cost?', context: 'Internal contract.' },
                    ],
                    decisions: decided('Compare on cost first', 'Cost is the reason the question came up.'),
                }),
                // Second step: one answer, one honest "cannot find out".
                call('finish_refinement', {
                    spec: 'Recommend a billing provider from public pricing; the current contract cost is unknown and must be stated as such.',
                    definition_of_done: ['A recommendation is given', 'The unknown contract cost is called out'],
                    decisions: decided('Proceed without the contract cost', 'It cannot be found; the recommendation says so.'),
                }),
            ] },
            // The two research subtasks, told apart by their question.
            { match: { phase: 'executor', contains: 'What does Stripely charge' }, replies: [
                // Held until the test has seen both questions in flight at once.
                { ...finishWork('done', 'Stripely charges 2.9% + 30c per transaction.', { evidence: ['stripely.example/pricing'] }), hold: true },
            ] },
            { match: { phase: 'executor', contains: 'current contract with Payzo' }, replies: [
                { ...finishWork('cannot_complete', 'The Payzo contract is not in any place I can read.', {
                    details: 'Searched the workspace and the artifacts; no contract document exists.',
                    dead_ends: [{ title: 'Looked for the contract in the repository', detail: 'No file mentions Payzo pricing.' }],
                }), hold: true },
            ] },
            { match: { phase: 'plan' }, replies: [
                call('create_tasks', {
                    tasks: [{ key: 'report', title: 'Write the recommendation', instructions: 'Write the recommendation from the Stripely pricing.', done_when: 'The recommendation exists.', type: 'research' }],
                    decisions: decided('One report task'),
                }),
            ] },
            { match: { phase: 'executor', contains: 'Write the recommendation' }, replies: [
                finishWork('done', 'Recommend Stripely: predictable public pricing. The Payzo contract cost is unknown.'),
            ] },
        ]);

        await startTask(request, task.id);
        // The two questions run side by side: both executor sessions are
        // waiting on the provider at the same moment.
        await expect.poll(async () => (await mockLog()).held, { timeout: 30_000, message: 'both research subtasks should be in flight together' }).toBe(2);
        await releaseMock();
        await waitForTaskStatus(request, task.id, 'in-review', 60_000);

        // ── The smart model is asked one question at a time, with no history ──
        const log = await mockLog();
        const smart = log.completions.filter((entry) => !['executor', 'checkpoint', 'text'].includes(entry.phase || ''));
        expect(smart.map((entry) => entry.phase)).toEqual(['refine', 'refine', 'plan', 'verify']);
        for (const entry of smart) {
            const body = entry.body as any;
            expect(body.model, 'smart steps run on the smart tier').toBe(SMART_MODEL);
            expect(body.messages.map((message: any) => message.role), 'a fresh prompt, never a chat history').toEqual(['system', 'user']);
            expect(body.tool_choice).toBe('required');
            const tools = body.tools.map((tool: any) => tool.function.name);
            // A tiny tool set: nothing that reads or writes files.
            for (const forbidden of ['bash', 'read', 'write', 'grep', 'ls', 'web_fetch', 'finish_work']) {
                expect(tools).not.toContain(forbidden);
            }
        }
        // The second refinement prompt was composed from scratch and carries
        // both answers, the unanswerable one marked with its reason.
        const secondRefine = promptText(smart[1]);
        expect(secondRefine).toContain('Stripely charges 2.9% + 30c per transaction.');
        expect(secondRefine).toContain('NOT ANSWERED');
        expect(secondRefine).toContain('The Payzo contract is not in any place I can read.');
        // The first prompt knew nothing of them.
        expect(promptText(smart[0])).not.toContain('Stripely charges 2.9%');

        // ── The cheap model did the work, with the full tool set ──
        const executors = log.completions.filter((entry) => entry.phase === 'executor');
        expect(executors).toHaveLength(3);
        for (const entry of executors) {
            const body = entry.body as any;
            expect(body.model, 'executors run on the cheap tier').toBe(CHEAP_MODEL);
            const tools = body.tools.map((tool: any) => tool.function.name);
            for (const expected of ['bash', 'read', 'write', 'grep', 'web_fetch', 'checkpoint', 'finish_work']) {
                expect(tools).toContain(expected);
            }
            // An executor never asks the human and never creates tasks.
            expect(tools).not.toContain('ask_human');
            expect(tools).not.toContain('create_tasks');
        }

        // ── Everything is a task: the questions and the report are subtasks ──
        const tree = await getTree(request, task.id);
        const subtasks = tree.slice(1);
        expect(subtasks.map((t: any) => [t.task_type, t.mode, t.status, t.origin_phase])).toEqual([
            ['research', 'direct', 'done', 'refine'],
            ['research', 'direct', 'failed', 'refine'],
            ['research', 'direct', 'done', 'plan'],
        ]);
        expect(subtasks[1].result_reason).toBe('cannot_complete');

        // ── The journal records every smart prompt separately ──
        const steps = await getSteps(request, task.id);
        expect(steps.filter((step: any) => step.kind === 'smart_call').map((step: any) => step.tool_name))
            .toEqual(['ask_questions', 'finish_refinement', 'create_tasks', 'finish_verification']);
        const refined = await getJSON(request, `/api/tasks/${task.id}`);
        expect(refined.refined_description).toContain('the current contract cost is unknown');

        // ── Decisions: the smart model's at the root, the executor's dead end under its subtask ──
        const decisions = await getDecisions(request, task.id);
        const titlesByTask = (id: number) => decisions.filter((d: any) => d.task_id === id).map((d: any) => d.title);
        expect(titlesByTask(task.id)).toEqual(expect.arrayContaining(['Compare on cost first', 'Proceed without the contract cost', 'One report task']));
        const deadEnds = decisions.filter((d: any) => d.kind === 'dead_end');
        expect(deadEnds).toHaveLength(1);
        expect(deadEnds[0].task_id).toBe(subtasks[1].id);

        // ── Usage: every call is billed, and research asked during refinement counts as refine ──
        const usage = await getUsage(request, task.id);
        expect(usage.totals.calls).toBe(log.completionsAnswered);
        const byPhase = Object.fromEntries(usage.groups.phase.map((group: any) => [group.key, group.calls]));
        expect(byPhase).toEqual({ refine: 4, plan: 1, execute: 1, verify: 1 });
        const byTier = Object.fromEntries(usage.groups.phase_tier.map((group: any) => [group.key, group.calls]));
        expect(byTier['refine/smart']).toBe(2);
        expect(byTier['refine/cheap']).toBe(2);

        // ── And the task page shows it ──
        await page.goto(`/companies/research-co/tasks/${task.id}`);
        await expect(page.getByTestId('task-status-label')).toHaveText('In review');
        await page.getByTestId('task-tab-workflow').click();
        const subtaskTree = page.getByTestId('subtask-tree');
        await expect(subtaskTree.getByTestId('tree-task')).toHaveCount(4);
        await expect(subtaskTree).toContainText('it could not be completed with what is available');
        await page.getByTestId('task-tab-decisions').click();
        await expect(page.getByTestId('decision-tree')).toContainText('Dead end');
        await expect(page.getByTestId('decision-tree')).toContainText('Proceed without the contract cost');
    });
});
