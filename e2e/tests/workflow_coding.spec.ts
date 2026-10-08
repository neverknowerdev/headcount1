import { test, expect } from '@playwright/test';
import { spawnSync } from 'child_process';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { resetE2E } from '../helpers/reset';
import { waitForTaskStatus } from '../helpers/wait-for';
import {
    CHEAP_MODEL, call, createTask, createWorkspace, decided, finishWork, getJSON, getSteps, getTree, getUsage, mockLog, promptText, setScenario, startTask,
} from '../helpers/workflow';
import type { Workspace } from '../helpers/workflow';

const env = loadE2EEnv();

const git = (dir: string, ...args: string[]): string => {
    const result = spawnSync('git', ['-C', dir, ...args], { encoding: 'utf8' });
    if (result.status !== 0) throw new Error(`git ${args.join(' ')} failed: ${result.stderr}`);
    return result.stdout.trim();
};

// A coding task from start to review, and then what it cost.
//
// The CTO designs, the QA Lead plans the tests, the CTO plans the work. Each
// implementation task is reviewed; when the review asks for changes the engine
// itself runs a fix and a second review, with no smart call in between.
test.describe.serial('Workflow: coding task', () => {
    let workspace: Workspace;
    let taskId: number;
    let servedForTask = 0;

    test.beforeAll(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
        workspace = await createWorkspace(request, 'Coding Co', 'coding-co');
    });

    test('design, test plan, implementation, a review that asks for changes, one engine-driven fix round, then review', async ({ request, page }) => {
        const projectRes = await request.post('/api/projects', {
            data: { company_id: workspace.companyId, name: 'Greeter', workspace_folder: 'coding-co/greeter', repository_url: env.E2E_TEST_REPO_URL },
        });
        expect(projectRes.ok(), await projectRes.text()).toBeTruthy();
        const project = await projectRes.json();
        const task = await createTask(request, workspace, {
            title: 'Greet the world', description: 'The project should have a greeting file.', task_type: 'coding', project_id: project.id,
        });
        taskId = task.id;

        await setScenario([
            { match: { phase: 'design' }, replies: [
                call('finish_design', { design: 'Add greeting.txt at the repository root holding one line of greeting.', decisions: decided('A plain text file', 'Nothing reads it programmatically.') }),
            ] },
            { match: { phase: 'test_plan' }, replies: [
                call('finish_test_plan', { test_scenarios: ['greeting.txt exists', 'It ends with an exclamation mark'], decisions: decided('Check presence and punctuation') }),
            ] },
            { match: { phase: 'plan' }, replies: [
                call('create_tasks', {
                    tasks: [{ key: 'greeting', title: 'Add the greeting', instructions: 'Create greeting.txt with a greeting.', done_when: 'greeting.txt exists.', type: 'coding' }],
                    decisions: decided('One implementation task'),
                }),
            ] },
            // Executor sessions, told apart by the header of their own task.
            { match: { phase: 'executor', contains: '— Add the greeting\n' }, replies: [
                call('write', { path: 'greeting.txt', content: 'Hello world\n' }),
                finishWork('done', 'Created greeting.txt.', { evidence: ['cat greeting.txt'] }),
            ] },
            { match: { phase: 'executor', contains: '— Review: Add the greeting\n' }, replies: [
                finishWork('done', 'The greeting has no exclamation mark; the test plan requires one.', { verdict: 'changes_requested' }),
                finishWork('done', 'The greeting now ends with an exclamation mark.', { verdict: 'approved' }),
            ] },
            { match: { phase: 'executor', contains: '— Address review findings (round 1): Add the greeting\n' }, replies: [
                call('write', { path: 'greeting.txt', content: 'Hello, world!\n' }),
                finishWork('done', 'Added the exclamation mark.'),
            ] },
            // The commit messages the cheap tier writes for each change.
            { match: { phase: 'text' }, replies: [{ text: 'Add the greeting file' }, { text: 'Punctuate the greeting' }] },
        ]);

        await startTask(request, taskId);
        await waitForTaskStatus(request, taskId, 'in-review', 120_000);

        // ── The smart steps, each spoken as the role the phase calls for ──
        const log = await mockLog();
        servedForTask = log.completionsAnswered;
        const smart = log.completions.filter((entry) => !['executor', 'checkpoint', 'text'].includes(entry.phase || ''));
        expect(smart.map((entry) => entry.phase), 'the review loop needed no smart call and no re-plan').toEqual(['refine', 'design', 'test_plan', 'plan', 'verify']);
        const speaker = (phase: string) => ((smart.find((entry) => entry.phase === phase)!.body as any).messages[0].content as string).split('\n')[0];
        expect(speaker('design')).toBe('You are the CTO agent.');
        expect(speaker('test_plan')).toBe('You are the QA Lead agent.');
        expect(speaker('plan')).toBe('You are the CTO agent.');
        // What each phase produced reaches the next one's prompt.
        expect(promptText(smart.find((entry) => entry.phase === 'plan')!)).toContain('Add greeting.txt at the repository root');
        expect(promptText(smart.find((entry) => entry.phase === 'plan')!)).toContain('It ends with an exclamation mark');

        const root = await getJSON(request, `/api/tasks/${taskId}`);
        expect(root.design).toContain('Add greeting.txt at the repository root');
        expect(JSON.parse(root.test_cases).map((item: any) => item.text)).toEqual(['greeting.txt exists', 'It ends with an exclamation mark']);

        // ── Implementation, review, fix, review ──
        const subtasks = (await getTree(request, taskId)).slice(1);
        expect(subtasks.map((t: any) => [t.title, t.task_type, t.status, t.result_verdict || ''])).toEqual([
            ['Add the greeting', 'coding', 'done', ''],
            ['Review: Add the greeting', 'review', 'done', 'changes_requested'],
            ['Address review findings (round 1): Add the greeting', 'coding', 'done', ''],
            ['Review: Add the greeting', 'review', 'done', 'approved'],
        ]);
        const steps = await getSteps(request, taskId);
        expect(steps.filter((step: any) => step.kind === 'review_round')).toHaveLength(1);
        // The fix was handed the findings it has to address.
        const fixPrompt = promptText(log.completions.find((entry) => promptText(entry).includes('— Address review findings (round 1)'))!);
        expect(fixPrompt).toContain('The greeting has no exclamation mark');

        // ── The code: both changes are in the task's worktree, committed in order ──
        const worktree = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1', 'workspace', 'coding-co', `task-${taskId}`);
        expect(fs.readFileSync(path.join(worktree, 'greeting.txt'), 'utf8')).toBe('Hello, world!\n');
        expect(git(worktree, 'status', '--porcelain')).toBe('');
        expect(git(worktree, 'rev-parse', '--abbrev-ref', 'HEAD')).toBe(root.github_branch);
        expect(git(worktree, 'log', '--format=%s', '-3').split('\n')).toEqual(['Punctuate the greeting', 'Add the greeting file', 'initial commit']);

        // Writers work in the shared worktree; reviewers each in a scratch
        // directory of their own, so they never get in a writer's way.
        const sessions = await Promise.all(subtasks.map((t: any) => getJSON(request, `/api/tasks/${t.id}/runs`)));
        expect(sessions.map((runs: any[]) => runs.length)).toEqual([1, 1, 1, 1]);
        expect(sessions[0][0].workspace_path).toBe(worktree);
        expect(sessions[2][0].workspace_path).toBe(worktree);
        expect(sessions[1][0].workspace_path).not.toBe(worktree);
        expect(sessions[3][0].workspace_path).not.toBe(sessions[1][0].workspace_path);
        for (const entry of log.completions.filter((e) => e.phase === 'executor' || e.phase === 'text')) {
            expect((entry.body as any).model).toBe(CHEAP_MODEL);
        }

        // ── On the task page ──
        await page.goto(`/companies/coding-co/tasks/${taskId}`);
        await expect(page.getByTestId('task-status-label')).toHaveText('In review');
        await expect(page.getByTestId('task-spec')).toContainText('Technical design');
        await page.getByTestId('task-tab-workflow').click();
        await expect(page.getByTestId('subtask-tree').getByTestId('tree-task')).toHaveCount(5);
        await expect(page.getByTestId('subtask-tree').getByTestId('tree-task-after').first()).toBeVisible();
        await expect(page.getByTestId('journal-step-review_round')).toContainText('starting fix round 1');
    });

    test('usage: totals match what the provider served, every breakdown adds up, and each call opens as a log', async ({ request, page }) => {
        // refine, design, test plan, plan, verify + 2+1+2+1 executor turns + 2 commit messages
        expect(servedForTask).toBe(13);
        const usage = await getUsage(request, taskId);
        expect(usage.totals.calls).toBe(servedForTask);
        expect(usage.totals.prompt_tokens).toBe(servedForTask * 100);
        expect(usage.totals.completion_tokens).toBe(servedForTask * 10);
        for (const dimension of ['task', 'phase', 'phase_tier', 'agent', 'model', 'tier']) {
            const sum = usage.groups[dimension].reduce((total: number, group: any) => total + group.calls, 0);
            expect(sum, `breakdown by ${dimension}`).toBe(servedForTask);
        }
        expect(Object.fromEntries(usage.groups.phase.map((group: any) => [group.key, group.calls])))
            .toEqual({ refine: 1, design: 1, test_plan: 1, plan: 1, execute: 8, verify: 1 });
        expect(Object.fromEntries(usage.groups.tier.map((group: any) => [group.key, group.calls]))).toEqual({ smart: 5, cheap: 6, commit: 2 });
        expect(usage.groups.agent.map((group: any) => group.label).sort()).toEqual(expect.arrayContaining(['CTO', 'Coder', 'QA Lead']));

        // ── The company's Usage page ──
        await page.goto('/companies/coding-co/usage');
        await expect(page.getByRole('heading', { name: 'Usage' })).toBeVisible();
        // 13 calls x 110 tokens
        await expect(page.getByTestId('usage-total-tokens')).toHaveText('1.4K');
        const bySteps = page.getByTestId('usage-by-phase');
        await expect(bySteps.getByTestId('usage-row')).toHaveCount(6);
        await expect(bySteps.getByTestId('usage-row').first()).toContainText('Refine');
        const executeRow = bySteps.getByTestId('usage-row').filter({ hasText: 'Execute' });
        await expect(executeRow).toContainText('8 calls');

        // A smart call opens as its full prompt and answer.
        await bySteps.getByText('Design', { exact: true }).click();
        await expect(bySteps.getByTestId('usage-call')).toHaveCount(1);
        await bySteps.getByTestId('usage-call').click();
        const log = page.getByTestId('call-log');
        await expect(log).toContainText('Smart call');
        await expect(log.getByTestId('smart-system')).toContainText('You are the CTO agent.');
        await expect(log.getByTestId('smart-prompt')).toContainText('Greet the world');
        await expect(log.getByTestId('smart-answer')).toContainText('Add greeting.txt at the repository root');
        await log.getByRole('button', { name: 'Close' }).click();
        await expect(log).toHaveCount(0);

        // An executor call opens as that turn of its session.
        const byModel = page.getByTestId('usage-by-model');
        await byModel.getByText(CHEAP_MODEL).click();
        await expect(byModel.getByTestId('usage-call')).toHaveCount(8);
        // Newest first: the last row is the implementation's first turn, the write.
        await byModel.getByTestId('usage-call').last().click();
        await expect(log).toContainText('Executor call');
        // Its brief and its one tool call, and nothing of the turn after it.
        await expect(log).toContainText('## Your task');
        await expect(log).toContainText('write');
        await expect(log).not.toContainText('finish_work1 tool');

        // The task's own usage panel counts its subtasks.
        await page.goto(`/companies/coding-co/tasks/${taskId}`);
        await page.getByTestId('task-tab-usage').click();
        await expect(page.getByTestId('task-usage').getByTestId('usage-total-tokens')).toHaveText('1.4K');
        await expect(page.getByTestId('task-usage').getByTestId('usage-by-task').getByTestId('usage-row')).toHaveCount(5);
    });
});
