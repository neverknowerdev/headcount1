import { test, expect } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';
import { resetE2E } from '../helpers/reset';
import { waitForTaskStatus } from '../helpers/wait-for';
import { call, createTask, createWorkspace, decided, finishWork, getJSON, getSteps, getTree, mockLog, setScenario, startTask, waitForTask } from '../helpers/workflow';

const env = loadE2EEnv();

// The provider refuses every call the executors make. That is not a result
// to plan around: the work stops after one batch, the error is brought to the
// top-level task and the human, and nothing is asked again until they reply.
test.describe('Workflow: a model that cannot be called', () => {
    test.beforeEach(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test('the task stops with the error, shows it, and carries on once the human replies', async ({ request, page }) => {
        const workspace = await createWorkspace(request, 'Outage Co', 'outage-co');
        const task = await createTask(request, workspace, { title: 'Research the current project', description: 'What is done and what is left before launch?', task_type: 'general' });

        const refusal = 'Request is missing x-opencode-session and cannot be routed efficiently.';
        const questions = {
            questions: [
                { question: 'What is the project name and what are its goals?' },
                { question: 'Which technology stack does the project use?' },
                { question: 'What does launch mean for this project?' },
            ],
        };
        await setScenario([
            { match: { phase: 'refine' }, replies: [call('ask_questions', questions)] },
            { match: { phase: 'executor' }, repeat: true, replies: [{ status: 400, text: refusal }] },
        ]);

        await startTask(request, task.id);
        const blocked = await waitForTask(request, task.id, (t) => t.status === 'blocked' && t.waiting_on === 'human', 'blocked on the human', 90_000);
        expect(blocked.phase, 'it stays in the phase it was in').toBe('refine');
        expect(blocked.wait_detail).toContain(refusal);

        // One batch of questions was sent out, each tried twice at most, and
        // the smart model was not asked to do anything about the outage.
        const log = await mockLog();
        expect(log.completions.filter((entry) => entry.phase === 'refine')).toHaveLength(1);
        const refused = log.completions.filter((entry) => entry.phase === 'executor');
        expect(refused.length).toBeGreaterThanOrEqual(4);
        expect(refused.length).toBeLessThanOrEqual(6);
        // Every call says which conversation it belongs to, and who is calling.
        for (const entry of log.completions) {
            expect(entry.session).toMatch(/^hc1-[0-9a-f]{32}$/);
            expect(entry.userAgent).toBe('headcount1');
        }
        expect(new Set(log.completions.filter((entry) => entry.phase === 'refine').map((entry) => entry.session)).size).toBe(1);

        const tree = await getTree(request, task.id);
        expect(tree).toHaveLength(4);
        const failed = tree.slice(1).filter((t: any) => t.status === 'failed');
        expect(failed.length).toBeGreaterThanOrEqual(1);
        for (const subtask of failed) expect(subtask.result_reason).toBe('model_error');
        for (const subtask of tree.slice(1)) expect(['failed', 'canceled']).toContain(subtask.status);
        const steps = await getSteps(request, task.id);
        expect(steps.map((step: any) => step.kind)).not.toContain('subtask_finished');

        // The error is on the top-level task, once, with how often it came.
        const errors = await getJSON(request, `/api/tasks/${task.id}/errors`);
        expect(errors.groups).toHaveLength(1);
        expect(errors.groups[0].kind).toBe('model_call');
        expect(errors.groups[0].message).toContain(refusal);
        expect(errors.groups[0].count).toBe(refused.length);
        expect(errors.groups[0].tasks.length).toBeGreaterThanOrEqual(1);

        await page.goto(`/companies/outage-co/tasks/${task.id}`);
        await expect(page.getByTestId('task-status-label')).toHaveText('Blocked');
        const dialogue = page.getByTestId('question-dialogue');
        await expect(dialogue).toContainText('the model could not be called');
        await expect(dialogue).toContainText(refusal);
        await expect(page.getByTestId('task-tab-errors')).toHaveText(`Errors (${refused.length})`);
        await page.getByTestId('task-tab-errors').click();
        await expect(page.getByTestId('task-error')).toHaveCount(1);
        await expect(page.getByTestId('task-error-message')).toContainText(refusal);
        await expect(page.getByTestId('task-error-count')).toHaveText(`${refused.length} times`);

        // The sessions are listed under their subtasks, under the task.
        await page.getByTestId('task-tab-workflow').click();
        await expect(page.getByTestId('tree-task-sessions').first()).toContainText('(failed)');
        await page.goto('/companies/outage-co/runs');
        const top = page.getByTestId('run-tree-task').first();
        await expect(top).toHaveAttribute('data-depth', '0');
        await expect(top).toContainText('Research the current project');
        await expect(top.getByTestId('run-tree-tally').first()).toContainText(`${refused.length} sessions`);
        // Each question got as far as a session before the batch was stopped.
        await expect(page.locator('[data-testid="run-tree-task"][data-depth="1"]')).toHaveCount(3);
        await expect(page.getByTestId('run-card')).toHaveCount(refused.length);

        // The archive is laid out as the tasks are, named by their keys.
        const archive = await request.get(`/api/tasks/${task.id}/logs/download`);
        expect(archive.ok()).toBeTruthy();
        expect(archive.headers()['content-disposition']).toContain(`${tree[0].ref_key}-logs.zip`);
        const names = (await archive.body()).toString('latin1');
        expect(names).toContain(`${tree[0].ref_key}/task.jsonl`);
        expect(names).toContain(`${tree[0].ref_key}/${failed[0].ref_key}/task.jsonl`);
        expect(names).toContain(`${tree[0].ref_key}/${failed[0].ref_key}/${failed[0].ref_key}-`);
        expect(names).not.toContain('/run-');

        // The provider is fixed and the human says so: the task carries on in
        // the same phase, and may ask again what never got to run.
        await setScenario([
            { match: { phase: 'refine' }, replies: [
                call('ask_questions', questions),
                call('finish_refinement', { spec: 'Report what is done and what is left.', definition_of_done: ['The report lists both'], decisions: decided('Report on both') }),
            ] },
            { match: { phase: 'executor' }, repeat: true, replies: [finishWork('done', 'Found it.')] },
        ]);
        await page.goto(`/companies/outage-co/tasks/${task.id}`);
        await page.getByTestId('question-dialogue').getByLabel('Your answer').fill('The provider is fixed, go on.');
        await page.getByTestId('question-dialogue').getByRole('button', { name: 'Answer' }).click();
        await waitForTaskStatus(request, task.id, 'in-review', 90_000);

        const outcomes = (await getSteps(request, task.id)).filter((step: any) => step.kind === 'subtask_finished').map((step: any) => step.result);
        expect(outcomes[0]).toBe('all 3 questions were answered');
    });

    test('a question that was answered is not sent out again', async ({ request }) => {
        const workspace = await createWorkspace(request, 'Repeat Co', 'repeat-co');
        const task = await createTask(request, workspace, { title: 'Describe the stack', description: 'Say what the project is built with.', task_type: 'general' });
        await setScenario([
            { match: { phase: 'refine' }, replies: [
                call('ask_questions', { questions: [{ question: 'Which technology stack (languages, frameworks) does the project use?' }] }),
                // The same thing again, reworded: refused, and the model has to move on.
                call('ask_questions', { questions: [{ question: 'Which technology stack does the project use (languages and frameworks)?' }] }),
                call('finish_refinement', { spec: 'Describe the stack.', definition_of_done: ['The stack is described'], decisions: decided('Describe it') }),
            ] },
            { match: { phase: 'executor' }, repeat: true, replies: [finishWork('done', 'Go and React.')] },
        ]);

        await startTask(request, task.id);
        await waitForTaskStatus(request, task.id, 'in-review', 90_000);

        const tree = await getTree(request, task.id);
        const asked = tree.filter((t: any) => t.origin_phase === 'refine');
        expect(asked, 'the question became one subtask, not two').toHaveLength(1);
        const refine = (await mockLog()).completions.filter((entry) => entry.phase === 'refine');
        expect(refine.length).toBeGreaterThanOrEqual(3);
        // The refusal went back to the model as a correction in the same step.
        expect(JSON.stringify(refine[2].body)).toContain('was already asked');
    });
});
