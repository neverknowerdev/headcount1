import { test, expect } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';
import { resetE2E } from '../helpers/reset';
import { waitForTaskStatus } from '../helpers/wait-for';
import { call, createTask, createWorkspace, decided, finishWork, getDecisions, getJSON, getTree, mockLog, setScenario, startTask } from '../helpers/workflow';

const env = loadE2EEnv();

// A long executor session is mostly tool calls. Its decisions are captured
// while it works, by checkpoints the engine demands, not left to a summary at
// the end; and a checkpoint that only restates a record adds nothing.
test.describe('Workflow: executor checkpoints', () => {
    test.beforeEach(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test('a 30-call session is made to checkpoint, and its records appear exactly once', async ({ request, page }) => {
        const workspace = await createWorkspace(request, 'Checkpoint Co', 'checkpoint-co');
        const task = await createTask(request, workspace, { title: 'Audit the configuration', description: 'Go through every file.', task_type: 'research' });

        // Thirty different calls: the same call over and over would be stopped as a loop.
        const thirtyCalls = Array.from({ length: 30 }, (_, i) => call('ls', { path: `config/part-${i + 1}` }));
        await setScenario([
            { match: { phase: 'plan' }, replies: [
                call('create_tasks', {
                    tasks: [{ key: 'audit', title: 'Walk the configuration', instructions: 'List and read every configuration file.', done_when: 'All files were looked at.', type: 'research' }],
                    decisions: decided('One pass over everything'),
                }),
            ] },
            // The executor never volunteers a checkpoint: it only lists files, then finishes.
            { match: { phase: 'executor', contains: 'Walk the configuration' }, replies: [...thirtyCalls, finishWork('done', 'All configuration files were listed.')] },
            // What it says when the engine demands one.
            { match: { phase: 'checkpoint' }, repeat: true, replies: [
                call('checkpoint', {
                    progress: 'Listed the first batch of files.',
                    decisions: [{ title: 'Read the files in directory order', detail: 'Top to bottom.', reason: 'Nothing suggests a better order.' }],
                    dead_ends: [{ title: 'Searching by file extension', detail: 'The files have no extensions.' }],
                    next_step: 'Continue listing.',
                }),
                // The same records again, restated: nothing new.
                call('checkpoint', {
                    progress: 'Listed the second batch of files.',
                    decisions: [{ title: 'Read the files in directory order', detail: 'Top to bottom.', reason: 'Nothing suggests a better order.' }],
                    dead_ends: [{ title: 'Searching by file extension', detail: 'The files have no extensions.' }],
                    next_step: 'Continue listing.',
                }),
            ] },
        ]);

        await startTask(request, task.id);
        await waitForTaskStatus(request, task.id, 'in-review', 120_000);

        const log = await mockLog();
        const forced = log.completions.filter((entry) => entry.phase === 'checkpoint');
        // 30 tool calls at one checkpoint per 12: the engine demanded at least two.
        expect(forced.length).toBeGreaterThanOrEqual(2);
        for (const entry of forced) {
            const body = entry.body as any;
            // For that one turn the model can do nothing else.
            expect(body.tools.map((tool: any) => tool.function.name)).toEqual(['checkpoint']);
            expect(body.tool_choice).toBe('required');
        }

        const subtask = (await getTree(request, task.id))[1];
        expect(subtask.status).toBe('done');
        const checkpoints = (await getJSON(request, `/api/tasks/${subtask.id}/steps`)).filter((step: any) => step.kind === 'checkpoint');
        expect(checkpoints.length).toBe(forced.length);
        expect(checkpoints[0].result).toContain('Listed the first batch of files.');

        // Each record exists once, however often it was restated.
        const records = (await getDecisions(request, task.id)).filter((d: any) => d.task_id === subtask.id);
        expect(records.map((d: any) => [d.kind, d.title]).sort()).toEqual([
            ['dead_end', 'Searching by file extension'],
            ['decision', 'Read the files in directory order'],
        ]);
        // The restatement did not vanish silently: the journal notes it.
        const notes = (await getJSON(request, `/api/tasks/${subtask.id}/steps`)).filter((step: any) => step.kind === 'note');
        expect(notes.some((step: any) => /duplicate/i.test(step.result))).toBe(true);

        await page.goto(`/companies/checkpoint-co/tasks/${task.id}`);
        await page.getByTestId('task-tab-decisions').click();
        await expect(page.getByTestId('decision-tree').getByTestId('decision').filter({ hasText: 'Read the files in directory order' })).toHaveCount(1);
        await expect(page.getByTestId('decision-tree').getByTestId('decision').filter({ hasText: 'Searching by file extension' })).toHaveCount(1);
    });
});
