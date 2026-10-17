import { test, expect } from '@playwright/test';
import { requireLiveEnv } from './helpers/live-env';
import { attachRunReport, createProjectInUI, createTaskInUI, expectRealUsage, onboardLive, startTaskInUI, waitForRest } from './helpers/live-ui';
import { changedFiles, runAcceptance, seedRepo, worktreeOf } from './helpers/repos';

// An existing codebase with a real bug and a failing test. The agents must
// find and fix the bug, and must not "fix" the test.
const SEED = {
    'package.json': JSON.stringify({ name: 'dates', version: '1.0.0', scripts: { test: 'node --test test/' } }, null, 2) + '\n',
    'src/dateRange.js': `// Returns every calendar day from start to end, both included, as YYYY-MM-DD.
function dateRange(start, end) {
    const days = [];
    const day = new Date(start + 'T00:00:00Z');
    const last = new Date(end + 'T00:00:00Z');
    while (day < last) {
        days.push(day.toISOString().slice(0, 10));
        day.setUTCDate(day.getUTCDate() + 1);
    }
    return days;
}

module.exports = { dateRange };
`,
    'test/dateRange.test.js': `const test = require('node:test');
const assert = require('node:assert');
const { dateRange } = require('../src/dateRange');

test('a three-day range includes both ends', () => {
    assert.deepStrictEqual(dateRange('2026-03-01', '2026-03-03'), ['2026-03-01', '2026-03-02', '2026-03-03']);
});
`,
};

const HIDDEN_TEST = `const test = require('node:test');
const assert = require('node:assert');
const { dateRange } = require('./src/dateRange');

test('hidden: a single day', () => assert.deepStrictEqual(dateRange('2026-05-05', '2026-05-05'), ['2026-05-05']));
test('hidden: across a month end in a leap year', () => assert.deepStrictEqual(dateRange('2028-02-28', '2028-03-01'), ['2028-02-28', '2028-02-29', '2028-03-01']));
test('hidden: end before start is empty', () => assert.deepStrictEqual(dateRange('2026-05-05', '2026-05-04'), []));
`;

test.describe('Live: bug fix in existing code', () => {
    let taskId: number | undefined;
    test.afterEach(async ({ request }, testInfo) => attachRunReport(request, testInfo, taskId));

    test('the failing test passes because the code was fixed, not the test', async ({ page, request }) => {
        const live = requireLiveEnv();
        const ws = await onboardLive(page, request, live, 'Live Bugfix', 'live-bugfix');
        await createProjectInUI(page, request, ws, 'Dates', seedRepo(SEED));
        const task = await createTaskInUI(page, request, ws, {
            title: 'npm test fails on dateRange',
            description: '`npm test` fails. Find the bug and fix it in the source. Do not change anything under test/.',
            type: 'coding',
            project: 'Dates',
        });
        taskId = task.id;
        await startTaskInUI(page, ws, task.id);

        await waitForRest(request, task.id, live.taskTimeoutMs);

        const worktree = worktreeOf(ws.shortName, task.id);
        const changed = changedFiles(worktree);
        expect(changed, 'the fix touched the source').toContain('src/dateRange.js');
        expect(changed.filter((f) => f.startsWith('test/')), 'the existing test was left alone').toEqual([]);

        const own = runAcceptance(worktree, {}, ['node', '--test', 'test/']);
        expect(own.status, own.output).toBe(0);
        const hidden = runAcceptance(worktree, { 'hidden.acceptance.test.js': HIDDEN_TEST }, ['node', '--test', 'hidden.acceptance.test.js']);
        expect(hidden.status, hidden.output).toBe(0);

        await expectRealUsage(request, task.id);
    });
});
