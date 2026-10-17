import { test, expect } from '@playwright/test';
import { requireLiveEnv } from './helpers/live-env';
import { attachRunReport, createProjectInUI, createTaskInUI, expectRealUsage, onboardLive, startTaskInUI, waitForRest } from './helpers/live-ui';
import { git, runAcceptance, seedRepo, worktreeOf } from './helpers/repos';

// A small feature big enough to be planned as several tasks, each built and
// reviewed. A hidden script drives the finished CLI end to end.
const HIDDEN_ACCEPTANCE = `const { execFileSync } = require('node:child_process');
const assert = require('node:assert');
const fs = require('node:fs');
const run = (...args) => execFileSync('node', ['todo.js', ...args], { encoding: 'utf8' });

fs.rmSync('todos.json', { force: true });
run('add', 'buy milk');
run('add', 'write tests');
let list = run('list');
assert.match(list, /buy milk/);
assert.match(list, /write tests/);
run('done', '1');
const stored = JSON.parse(fs.readFileSync('todos.json', 'utf8'));
const items = Array.isArray(stored) ? stored : stored.todos || stored.items;
assert.strictEqual(items.length, 2, 'both items are stored');
assert.ok(items.some((item) => /buy milk/.test(JSON.stringify(item)) && /true|done|completed/i.test(JSON.stringify(item))), 'item 1 is marked done');
list = run('list');
assert.match(list, /write tests/);
console.log('acceptance ok');
`;

test.describe('Live: multi-agent feature @full', () => {
    let taskId: number | undefined;
    test.afterEach(async ({ request }, testInfo) => attachRunReport(request, testInfo, taskId));

    test('a planned feature is split into reviewed subtasks and works end to end', async ({ page, request }) => {
        const live = requireLiveEnv();
        test.setTimeout(Math.max(live.taskTimeoutMs * 3, 60 * 60 * 1000));
        const ws = await onboardLive(page, request, live, 'Live Team', 'live-team');
        await createProjectInUI(page, request, ws, 'Todo', seedRepo({}));
        const task = await createTaskInUI(page, request, ws, {
            title: 'Build a todo CLI',
            description: 'Build a small command-line todo app in plain Node.js (no dependencies), run as `node todo.js <command>`:\n' +
                '- `add <text>` appends an item;\n' +
                '- `list` prints the items, numbered from 1, showing which are done;\n' +
                '- `done <n>` marks item n as done.\n' +
                'Items persist in todos.json in the current directory. Also write a README describing the commands, ' +
                'and tests runnable with `node --test`. Split the work so the storage, the commands and the docs can be built separately.',
            type: 'coding',
            project: 'Todo',
        });
        taskId = task.id;
        await startTaskInUI(page, ws, task.id);

        await waitForRest(request, task.id, Math.max(live.taskTimeoutMs * 3, 60 * 60 * 1000) - 5 * 60_000);

        const tree = await (await request.get(`/api/tasks/${task.id}/tree`)).json() as any[];
        const implementation = tree.slice(1).filter((t) => t.task_type === 'coding' && !/^Address review findings/.test(t.title));
        const reviews = tree.slice(1).filter((t) => t.task_type === 'review');
        expect(implementation.length, 'the plan split the feature').toBeGreaterThanOrEqual(2);
        expect(reviews.length, 'the work was reviewed').toBeGreaterThanOrEqual(1);

        const decisions = await (await request.get(`/api/tasks/${task.id}/decisions?subtree=true`)).json() as any[];
        expect(new Set(decisions.map((d) => d.task_id)).size, 'decisions recorded across the tree').toBeGreaterThan(1);

        const worktree = worktreeOf(ws.shortName, task.id);
        expect(git(worktree, 'status', '--porcelain')).toBe('');
        const own = runAcceptance(worktree, {}, ['node', '--test']);
        expect(own.status, own.output).toBe(0);
        const hidden = runAcceptance(worktree, { 'hidden-acceptance.js': HIDDEN_ACCEPTANCE }, ['node', 'hidden-acceptance.js']);
        expect(hidden.status, hidden.output).toBe(0);

        const usage = await expectRealUsage(request, task.id);
        expect(usage.groups.agent.length, 'several roles worked on it').toBeGreaterThan(2);
    });
});
