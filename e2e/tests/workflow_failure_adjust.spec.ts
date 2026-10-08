import { test, expect } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';
import { resetE2E } from '../helpers/reset';
import { waitForTaskStatus } from '../helpers/wait-for';
import {
    call, createTask, createWorkspace, decided, finishWork, getDecisions, getSteps, getTree, mockLog, promptText, setScenario, startTask,
} from '../helpers/workflow';

const env = loadE2EEnv();

// When a subtask fails, what depended on it is canceled rather than left
// waiting, and the smart model re-plans with the full picture: what failed,
// why, and what was decided so far.
test.describe('Workflow: failure and adjustment', () => {
    test.beforeEach(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test('a failed subtask cancels its dependents and sends the task to adjust, which re-plans', async ({ request, page }) => {
        const workspace = await createWorkspace(request, 'Adjust Co', 'adjust-co');
        const task = await createTask(request, workspace, { title: 'Publish the price list', description: 'Export prices and publish them.', task_type: 'general' });

        await setScenario([
            { match: { phase: 'plan' }, replies: [
                call('create_tasks', {
                    tasks: [
                        { key: 'export', title: 'Export from the legacy system', instructions: 'Export the prices from the legacy system.', done_when: 'A CSV exists.', type: 'general' },
                        { key: 'publish', title: 'Publish the export', instructions: 'Publish the CSV.', done_when: 'It is published.', type: 'general', depends_on: ['export'] },
                    ],
                    decisions: decided('Export first, then publish', 'Publishing needs the export.'),
                }),
            ] },
            { match: { phase: 'executor', contains: 'Export from the legacy system' }, repeat: true, replies: [
                finishWork('failed', 'The legacy system rejects every login.', {
                    details: 'HTTP 401 on all three documented endpoints.',
                    dead_ends: [{ title: 'Logging in to the legacy system', detail: 'Every documented endpoint returns 401.' }],
                }),
            ] },
            { match: { phase: 'adjust' }, replies: [
                // First it asks to read the decision tree; that changes nothing.
                call('get_decision_tree', { scope: 'root' }),
                // Then it replaces the plan.
                call('create_tasks', {
                    tasks: [{ key: 'manual', title: 'Publish from the spreadsheet', instructions: 'Use the finance spreadsheet instead of the legacy export and publish it.', done_when: 'It is published.', type: 'general' }],
                    decisions: decided('Use the spreadsheet instead of the legacy export', 'The legacy system cannot be reached.'),
                }),
            ] },
            { match: { phase: 'executor', contains: 'Publish from the spreadsheet' }, replies: [finishWork('done', 'Published from the spreadsheet.')] },
        ]);

        await startTask(request, task.id);
        await waitForTaskStatus(request, task.id, 'in-review', 90_000);

        const tree = await getTree(request, task.id);
        const byTitle = Object.fromEntries(tree.map((t: any) => [t.title, t]));
        expect(byTitle['Export from the legacy system'].status).toBe('failed');
        expect(byTitle['Export from the legacy system'].result_reason).toBe('reported_failure');
        // The dependent never ran: it is canceled, with the reason.
        expect(byTitle['Publish the export'].status).toBe('canceled');
        expect(byTitle['Publish the export'].result_reason).toBe('prerequisite_failed');
        expect(byTitle['Publish from the spreadsheet'].status).toBe('done');
        expect(byTitle['Publish from the spreadsheet'].origin_phase).toBe('adjust');

        const log = await mockLog();
        // The canceled task was never given to an executor.
        expect(log.completions.some((entry) => entry.phase === 'executor' && promptText(entry).includes('## Your task\n\n') && promptText(entry).includes('Publish the export\n'))).toBe(false);

        // The adjust prompts: the first already says what failed and why; the
        // second also carries the decision tree it asked for.
        const adjust = log.completions.filter((entry) => entry.phase === 'adjust');
        expect(adjust).toHaveLength(2);
        expect(promptText(adjust[0])).toContain('The legacy system rejects every login.');
        expect(promptText(adjust[0])).toContain('Publish the export');
        expect(promptText(adjust[1])).toContain('Output of get_decision_tree');
        expect(promptText(adjust[1])).toContain('Export first, then publish');
        expect(promptText(adjust[1])).toContain('Logging in to the legacy system');
        for (const entry of adjust) expect((entry.body as any).messages).toHaveLength(2);

        // The journal shows the path: plan, execute, adjust (twice), execute, verify.
        const steps = await getSteps(request, task.id);
        expect(steps.filter((step: any) => step.kind === 'smart_call').map((step: any) => step.tool_name))
            .toEqual(['finish_refinement', 'create_tasks', 'get_decision_tree', 'create_tasks', 'finish_verification']);
        expect(steps.filter((step: any) => step.kind === 'phase_entered').map((step: any) => step.phase)).toContain('adjust');

        const decisions = await getDecisions(request, task.id);
        expect(decisions.map((d: any) => d.title)).toEqual(expect.arrayContaining([
            'Export first, then publish', 'Logging in to the legacy system', 'Use the spreadsheet instead of the legacy export',
        ]));

        await page.goto(`/companies/adjust-co/tasks/${task.id}`);
        await page.getByTestId('task-tab-workflow').click();
        const subtaskTree = page.getByTestId('subtask-tree');
        await expect(subtaskTree).toContainText('a task it depends on did not succeed');
        await expect(subtaskTree).toContainText('from adjust');
        await expect(page.getByTestId('task-journal').getByTestId('journal-step-smart_call')).toHaveCount(5);
    });

    test('stopping a task cancels what runs beneath it, and running it again continues from what it has', async ({ request }) => {
        const workspace = await createWorkspace(request, 'Stop Co', 'stop-co');
        const task = await createTask(request, workspace, { title: 'A long job', description: 'Takes a while.', task_type: 'general' });
        await setScenario([
            // The subtask's executor never answers until released.
            { match: { phase: 'executor' }, replies: [{ ...finishWork('done', 'late'), hold: true }] },
        ]);

        await startTask(request, task.id);
        await expect.poll(async () => (await mockLog()).held, { timeout: 30_000 }).toBe(1);
        const running = await getTree(request, task.id);
        expect(running[1].status).toBe('in-progress');

        const stop = await request.post(`/api/tasks/${task.id}/stop`);
        expect(stop.ok(), await stop.text()).toBeTruthy();
        const stopped = await stop.json();
        expect(stopped.status).toBe('blocked');
        expect(stopped.waiting_on).toBe('operator');
        const after = await getTree(request, task.id);
        expect(after[1].status).toBe('canceled');
        expect(after[1].result_reason).toBe('stopped');

        // While stopped it can be placed by hand; a running task cannot.
        const direct = await request.put(`/api/tasks/${task.id}`, { data: { status: 'in-progress' } });
        expect(direct.status()).toBe(409);

        // Running it again re-plans from the specification it already has.
        await setScenario([]);
        const rerun = await request.post(`/api/tasks/${task.id}/rerun`);
        expect(rerun.ok(), await rerun.text()).toBeTruthy();
        await waitForTaskStatus(request, task.id, 'in-review', 60_000);
        const phases = (await getSteps(request, task.id)).filter((step: any) => step.kind === 'rerun' || step.kind === 'stopped').map((step: any) => step.kind);
        expect(phases).toEqual(['stopped', 'rerun']);
    });
});
