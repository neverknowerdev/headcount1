import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { requireLiveEnv } from './helpers/live-env';
import { attachRunReport, createProjectInUI, createTaskInUI, onboardLive, startTaskInUI, waitForRest } from './helpers/live-ui';
import { seedRepo, worktreeOf } from './helpers/repos';

// A decision only the owner can make. The workflow must stop and ask, the
// person answers on the task page, and the work follows the answer.
test.describe('Live: a question for the owner', () => {
    let taskId: number | undefined;
    test.afterEach(async ({ request }, testInfo) => attachRunReport(request, testInfo, taskId));

    test('the task asks which license to use and applies the answer', async ({ page, request }) => {
        const live = requireLiveEnv();
        const ws = await onboardLive(page, request, live, 'Live Question', 'live-question');
        await createProjectInUI(page, request, ws, 'Licensed', seedRepo({}));
        const task = await createTaskInUI(page, request, ws, {
            title: 'Add a license',
            description: 'Add a LICENSE file at the repository root. The choice of license is a business decision: ' +
                'ask the owner which license to use before writing it, and do not guess.',
            type: 'coding',
            project: 'Licensed',
        });
        taskId = task.id;
        await startTaskInUI(page, ws, task.id);

        const blocked = await waitForRest(request, task.id, live.taskTimeoutMs, (t) => t.status === 'blocked' && t.waiting_on === 'human');
        expect(blocked.status).toBe('blocked');

        await page.goto(`/companies/${ws.shortName}/tasks/${task.id}`);
        await expect(page.getByTestId('task-status-label')).toHaveText('Blocked');
        const dialogue = page.getByTestId('question-dialogue').filter({ has: page.getByTestId('question-answer-form') }).first();
        await expect(dialogue).toContainText(/licen[cs]e/i);
        await dialogue.getByLabel('Your answer').fill('Use the MIT License, copyright holder "Headcount1 E2E", year 2026.');
        await dialogue.getByRole('button', { name: 'Answer' }).click();

        // It may ask again; answer any follow-up the same way, a few times at most.
        for (let round = 0; round < 3; round++) {
            const next = await waitForRest(request, task.id, live.taskTimeoutMs,
                (t) => t.status === 'in-review' || (t.status === 'blocked' && t.waiting_on === 'human'));
            if (next.status === 'in-review') break;
            await page.reload();
            const open = page.getByTestId('question-dialogue').filter({ has: page.getByTestId('question-answer-form') }).first();
            await open.getByLabel('Your answer').fill('MIT License, "Headcount1 E2E", 2026. Nothing else is needed.');
            await open.getByRole('button', { name: 'Answer' }).click();
        }
        await waitForRest(request, task.id, live.taskTimeoutMs);

        const license = fs.readFileSync(path.join(worktreeOf(ws.shortName, task.id), 'LICENSE'), 'utf8');
        expect(license).toContain('MIT License');
        expect(license).toContain('Headcount1 E2E');

        const kinds = (await (await request.get(`/api/tasks/${task.id}/steps`)).json() as any[]).map((s) => s.kind);
        expect(kinds).toContain('human_question');
        expect(kinds).toContain('human_answer');
    });
});
