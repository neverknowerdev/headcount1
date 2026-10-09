import { test, expect } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';
import { resetE2E } from '../helpers/reset';
import { waitForTaskStatus } from '../helpers/wait-for';
import { call, createTask, createWorkspace, decided, getSteps, mockLog, promptText, setScenario, startTask, waitForTask } from '../helpers/workflow';

const env = loadE2EEnv();

// Only the smart model may ask the human, and only when it judges the question
// important. The task then stops, visibly, until that question is answered.
test.describe('Workflow: a question for the human', () => {
    test.beforeEach(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test('the task blocks on its question and resumes with the answer', async ({ request, page }) => {
        const workspace = await createWorkspace(request, 'Question Co', 'question-co');
        const task = await createTask(request, workspace, { title: 'Draft the launch announcement', description: 'Write it.', task_type: 'general' });

        await setScenario([
            { match: { phase: 'refine' }, replies: [
                call('ask_human', {
                    question: 'Which launch date should the announcement name?',
                    why: 'The date is a business decision nobody but you can make.',
                    decisions: decided('Ask for the launch date', 'It cannot be researched.'),
                }),
                call('finish_refinement', {
                    spec: 'Write the announcement for the launch on the date the human gave.',
                    definition_of_done: ['The announcement names the launch date'],
                    decisions: decided('Use the date from the human'),
                }),
            ] },
        ]);

        await startTask(request, task.id);
        const blocked = await waitForTask(request, task.id, (t) => t.status === 'blocked' && t.waiting_on === 'human', 'blocked on the human');
        expect(blocked.phase).toBe('refine');
        // Nothing runs while it waits: the provider is not called again.
        const callsWhileWaiting = (await mockLog()).completionsReceived;
        expect(callsWhileWaiting).toBe(1);

        // The question is on the task, with a reply box of its own.
        await page.goto(`/companies/question-co/tasks/${task.id}`);
        await expect(page.getByTestId('task-status-label')).toHaveText('Blocked');
        await expect(page.getByTestId('phase-chip')).toContainText('waiting for your answer');
        const dialogue = page.getByTestId('question-dialogue');
        await expect(dialogue).toContainText('Which launch date should the announcement name?');
        // A comment that is not an answer to this question does not release
        // the task when another question is the one being answered... here
        // there is only one, so answer it in place.
        await dialogue.getByLabel('Your answer').fill('The 3rd of March.');
        await dialogue.getByRole('button', { name: 'Answer' }).click();

        await waitForTaskStatus(request, task.id, 'in-review', 60_000);
        await expect(page.getByTestId('task-status-label')).toHaveText('In review', { timeout: 15_000 });
        await expect(dialogue).toContainText('The 3rd of March.');
        await expect(dialogue.getByTestId('question-answer-form')).toHaveCount(0);

        // The answer reached the next smart step, in a freshly composed prompt.
        const refineCalls = (await mockLog()).completions.filter((entry) => entry.phase === 'refine');
        expect(refineCalls).toHaveLength(2);
        expect(promptText(refineCalls[1])).toContain('Which launch date should the announcement name?');
        expect(promptText(refineCalls[1])).toContain('The 3rd of March.');
        expect((refineCalls[1].body as any).messages).toHaveLength(2);

        const kinds = (await getSteps(request, task.id)).map((step: any) => step.kind);
        expect(kinds).toContain('human_question');
        expect(kinds).toContain('human_answer');
        expect(kinds.indexOf('human_question')).toBeLessThan(kinds.indexOf('human_answer'));
    });

    test('a task with no model configured waits visibly and continues once one is set', async ({ request }) => {
        const workspace = await createWorkspace(request, 'Unconfigured Co', 'unconfigured-co');
        // Take the smart tier away.
        const cleared = await request.put('/api/default-model-settings/smart', { data: { provider_id: null, model: '', model_group_id: null } });
        expect(cleared.ok(), await cleared.text()).toBeTruthy();
        const task = await createTask(request, workspace, { title: 'Needs a model', description: 'Anything.' });

        await startTask(request, task.id);
        const waiting = await waitForTask(request, task.id, (t) => t.waiting_on === 'config', 'waiting for a model');
        expect(waiting.status).toBe('in-progress');
        expect(waiting.wait_detail).toContain('smart');
        expect((await mockLog()).completionsReceived).toBe(0);

        // Setting the model is all it takes; nobody has to restart the task.
        const restored = await request.put('/api/default-model-settings/smart', { data: { provider_id: workspace.providerId, model: 'e2e-smart-model' } });
        expect(restored.ok(), await restored.text()).toBeTruthy();
        await waitForTaskStatus(request, task.id, 'in-review', 60_000);

        // A task can also name its own model, which then runs its smart steps.
        const own = await createTask(request, workspace, { title: 'Runs on its own model', description: 'Anything.', provider_id: workspace.providerId, model: 'e2e-other-model' });
        await startTask(request, own.id);
        await waitForTaskStatus(request, own.id, 'in-review', 60_000);
        const ownCalls = (await mockLog()).completions.filter((entry) => promptText(entry).includes('Runs on its own model') && entry.phase !== 'executor');
        expect(ownCalls.length).toBeGreaterThan(0);
        for (const entry of ownCalls) expect((entry.body as any).model).toBe('e2e-other-model');
    });
});
