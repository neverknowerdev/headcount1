import { test, expect } from '@playwright/test';
import * as http from 'http';
import { AddressInfo } from 'net';
import { requireLiveEnv } from './helpers/live-env';
import { attachRunReport, createTaskInUI, expectRealUsage, onboardLive, startTaskInUI, waitForRest } from './helpers/live-ui';

// The cheapest full pass: onboard, create a research task, run it with real
// models, and check facts the answer must contain. The page is served by the
// test itself with values made up for this run, so no model can know them
// without fetching it, and the run needs no internet beyond the provider.
test.describe('Live: research smoke', () => {
    let taskId: number | undefined;
    let server: http.Server;
    let pageUrl = '';
    const codename = `Project ${['Heron', 'Lynx', 'Otter', 'Marten'][Date.now() % 4]}-${Math.floor(1000 + Math.random() * 9000)}`;
    const launchDay = `2031-0${1 + (Date.now() % 9)}-1${Date.now() % 10}`;

    test.beforeAll(async () => {
        server = http.createServer((_req, res) => {
            res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
            res.end(`<!doctype html><html><head><title>Internal launch note</title></head><body>
<h1>Internal launch note</h1>
<p>The next product ships under the codename <strong>${codename}</strong>.</p>
<p>Launch is planned for <time>${launchDay}</time>.</p>
<p>Questions go to <a href="mailto:launch@example.org">launch@example.org</a>.</p>
</body></html>`);
        });
        await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
        pageUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}/launch-note`;
    });
    test.afterAll(async () => { await new Promise((resolve) => server?.close(resolve)); });
    test.afterEach(async ({ request }, testInfo) => attachRunReport(request, testInfo, taskId));

    test('a research task reads a page and reports what is on it', async ({ page, request }) => {
        const live = requireLiveEnv();
        const ws = await onboardLive(page, request, live, 'Live Research', 'live-research');
        const task = await createTaskInUI(page, request, ws, {
            title: 'Summarize the launch note',
            description: `Read the internal launch note at ${pageUrl} and report the product codename, the launch date ` +
                'and the contact address it gives, each exactly as written there. Save the answer as a short Markdown artifact named answer.md.',
            type: 'research',
        });
        taskId = task.id;
        await startTaskInUI(page, ws, task.id);

        const done = await waitForRest(request, task.id, live.taskTimeoutMs);

        // The answer is somewhere the person reads it: an artifact or the result.
        const artifacts = await (await request.get(`/api/tasks/${task.id}/artifacts`)).json() as any[];
        const answer = [done.result_summary, done.result_details, ...artifacts.map((a) => a.content)].join('\n');
        expect(answer).toContain(codename);
        expect(answer).toContain(launchDay);
        expect(answer).toContain('launch@example.org');
        expect(artifacts.length, 'the research saved an artifact').toBeGreaterThan(0);

        // The workflow ran its phases and planned real work beneath the task.
        const tree = await (await request.get(`/api/tasks/${task.id}/tree`)).json() as any[];
        expect(tree.length, 'at least one subtask').toBeGreaterThan(1);
        const smart = (await (await request.get(`/api/tasks/${task.id}/steps`)).json() as any[]).filter((s) => s.kind === 'smart_call').map((s) => s.tool_name);
        expect(smart).toContain('finish_refinement');
        expect(smart).toContain('finish_verification');
        await expectRealUsage(request, task.id);

        // The task page shows the result.
        await page.goto(`/companies/${ws.shortName}/tasks/${task.id}`);
        await expect(page.getByTestId('task-status-label')).toHaveText('In review');
        await expect(page.getByTestId('task-result')).toBeVisible();
        await page.getByTestId('task-tab-usage').click();
        await expect(page.getByTestId('usage-by-phase')).toContainText('Refine');
    });
});
