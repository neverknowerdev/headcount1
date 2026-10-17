import { test, expect } from '@playwright/test';
import { requireLiveEnv } from './helpers/live-env';
import { attachRunReport, createProjectInUI, createTaskInUI, expectRealUsage, onboardLive, startTaskInUI, waitForRest } from './helpers/live-ui';
import { git, runAcceptance, seedRepo, worktreeOf } from './helpers/repos';

// A hidden test the agents never see decides whether the code works.
const HIDDEN_TEST = `
const test = require('node:test');
const assert = require('node:assert');
const mod = require('./slugify.js');
const slugify = typeof mod === 'function' ? mod : mod.slugify;

test('hidden: basic words', () => assert.strictEqual(slugify('Hello World'), 'hello-world'));
test('hidden: punctuation and spaces collapse', () => assert.strictEqual(slugify('  Hello,   World!  '), 'hello-world'));
test('hidden: accents become ASCII', () => assert.strictEqual(slugify('Crème Brûlée'), 'creme-brulee'));
test('hidden: digits are kept', () => assert.strictEqual(slugify('Top 10 Tips'), 'top-10-tips'));
test('hidden: nothing left gives an empty string', () => assert.strictEqual(slugify('!!!'), ''));
`;

test.describe('Live: small coding task', () => {
    let taskId: number | undefined;
    test.afterEach(async ({ request }, testInfo) => attachRunReport(request, testInfo, taskId));

    test('design, test plan, implementation and review produce working, committed code', async ({ page, request }) => {
        const live = requireLiveEnv();
        const ws = await onboardLive(page, request, live, 'Live Coding', 'live-coding');
        const repo = seedRepo({ 'package.json': JSON.stringify({ name: 'slug', version: '1.0.0', scripts: { test: 'node --test' } }, null, 2) + '\n' });
        await createProjectInUI(page, request, ws, 'Slug', repo);
        const task = await createTaskInUI(page, request, ws, {
            title: 'Add a slugify helper',
            description: 'Add slugify.js at the repository root, exporting slugify(text) via module.exports (CommonJS, no dependencies). ' +
                'It lowercases, turns accented letters into plain ASCII, keeps letters and digits, and joins words with single hyphens, ' +
                'with no leading or trailing hyphen. Add tests runnable with `node --test`.',
            type: 'coding',
            project: 'Slug',
        });
        taskId = task.id;
        await startTaskInUI(page, ws, task.id);

        const done = await waitForRest(request, task.id, live.taskTimeoutMs);

        // The coding workflow's own phases ran, each as its role.
        expect(done.design, 'the CTO wrote a design').toBeTruthy();
        expect(done.test_cases, 'the QA Lead wrote a test plan').toBeTruthy();
        const tree = await (await request.get(`/api/tasks/${task.id}/tree`)).json() as any[];
        const subtasks = tree.slice(1);
        expect(subtasks.some((t) => t.task_type === 'coding' && t.status === 'done'), 'an implementation subtask finished').toBeTruthy();
        const reviews = subtasks.filter((t) => t.task_type === 'review');
        expect(reviews.length, 'the implementation was reviewed').toBeGreaterThan(0);
        expect(reviews[reviews.length - 1].result_verdict).toBe('approved');

        // The work is committed on the task branch, and nothing is left loose.
        const worktree = worktreeOf(ws.shortName, task.id);
        expect(git(worktree, 'status', '--porcelain')).toBe('');
        expect(git(worktree, 'rev-parse', '--abbrev-ref', 'HEAD')).toBe(done.github_branch);
        expect(Number(git(worktree, 'rev-list', '--count', 'HEAD')), 'commits beyond the seed').toBeGreaterThan(1);

        // The agents' own tests pass, and so does the hidden one.
        const own = runAcceptance(worktree, {}, ['node', '--test']);
        expect(own.status, own.output).toBe(0);
        const hidden = runAcceptance(worktree, { 'hidden.acceptance.test.js': HIDDEN_TEST }, ['node', '--test', 'hidden.acceptance.test.js']);
        expect(hidden.status, hidden.output).toBe(0);

        await expectRealUsage(request, task.id);
    });
});
