/**
 * E2E tests for the web_fetch and browser_use agent tools.
 *
 * Each test spins up a tiny in-process HTTP server that the agent tools
 * target, configures the mock LLM provider with a scenario (an ordered list
 * of tool calls / text replies), then runs a full task end-to-end and
 * inspects what the agent sent back to the LLM.
 */

import * as http from 'http';
import * as net from 'net';
import { AddressInfo } from 'net';
import { test, expect } from '@playwright/test';
import type { APIRequestContext } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';
import type { ScenarioEntry } from '../fixtures/mock-provider-server';

const env = loadE2EEnv();

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Start a minimal HTTP server that always returns the given HTML body. */
async function startTestServer(html: string): Promise<{ url: string; stop: () => Promise<void> }> {
    const server = http.createServer((_req, res) => {
        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        res.end(html);
    });
    const port = await getFreePort();
    await new Promise<void>((resolve) => server.listen(port, '127.0.0.1', () => resolve()));
    const addr = server.address() as AddressInfo;
    return {
        url: `http://127.0.0.1:${addr.port}`,
        stop: () => new Promise<void>((resolve) => server.close(() => resolve())),
    };
}

/** Start an HTTP server that serves different responses per path. */
async function startMultiRouteServer(routes: Record<string, string>): Promise<{
    url: string;
    stop: () => Promise<void>;
}> {
    const server = http.createServer((req, res) => {
        const body = routes[req.url ?? '/'] ?? routes['/'] ?? 'Not found';
        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        res.end(body);
    });
    const port = await getFreePort();
    await new Promise<void>((resolve) => server.listen(port, '127.0.0.1', () => resolve()));
    const addr = server.address() as AddressInfo;
    return {
        url: `http://127.0.0.1:${addr.port}`,
        stop: () => new Promise<void>((resolve) => server.close(() => resolve())),
    };
}

function getFreePort(): Promise<number> {
    return new Promise((resolve, reject) => {
        const srv = net.createServer();
        srv.unref();
        srv.on('error', reject);
        srv.listen(0, '127.0.0.1', () => {
            const { port } = srv.address() as AddressInfo;
            srv.close(() => resolve(port));
        });
    });
}

/** Configure the mock LLM provider to run a specific scenario. */
async function setScenario(request: APIRequestContext, entries: ScenarioEntry[]): Promise<void> {
    const res = await request.post(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
        data: { entries },
    });
    expect(res.ok(), `set-scenario failed: ${await res.text()}`).toBeTruthy();
}

/** Reset the mock LLM provider to default mode and clear recorded requests. */
async function resetMockProvider(request: APIRequestContext): Promise<void> {
    await request.post(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`);
}

/** Return all requests the mock LLM provider has received. */
async function getMockRequests(request: APIRequestContext): Promise<any[]> {
    const res = await request.get(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`);
    const data = await res.json();
    return data.requests ?? [];
}

/**
 * Set up a fresh company + LLM provider + agent via the REST API.
 * Returns the IDs needed to create and run tasks.
 */
async function setupWorkspace(request: APIRequestContext, shortName: string): Promise<{
    companyId: number;
    agentId: number;
    sprintId: number;
}> {
    const compRes = await request.post('/api/companies', {
        data: { name: `Tool Test Co (${shortName})`, short_name: shortName, color: '#0ea5e9' },
    });
    expect(compRes.ok(), `create company: ${await compRes.text()}`).toBeTruthy();
    const company = await compRes.json();

    const sprintRes = await request.post('/api/sprints', {
        data: {
            company_id: company.id,
            name: 'Test Sprint',
            goal: '',
            start_date: '2024-01-01T00:00:00Z',
            end_date: '2024-12-31T00:00:00Z',
        },
    });
    expect(sprintRes.ok(), `create sprint: ${await sprintRes.text()}`).toBeTruthy();
    const sprint = await sprintRes.json();

    const provRes = await request.post('/api/providers', {
        data: {
            name: 'Mock Provider',
            base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key',
            provider_type: 'openai',
            default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model',
        },
    });
    expect(provRes.ok(), `create provider: ${await provRes.text()}`).toBeTruthy();
    const provider = await provRes.json();

    const agentRes = await request.post('/api/agents', {
        data: {
            company_id: company.id,
            name: 'QA',
            role_key: 'QA',
            short_name: 'QA',
            model: 'e2e-mock-model',
            provider_id: provider.id,
        },
    });
    expect(agentRes.ok(), `create agent: ${await agentRes.text()}`).toBeTruthy();
    const agent = await agentRes.json();

    return { companyId: company.id, agentId: agent.id, sprintId: sprint.id };
}

/**
 * Create a task in backlog, assign the agent, then set status → 'to-do'
 * which triggers the engine to start a run. Returns taskId.
 */
async function runTask(
    request: APIRequestContext,
    companyId: number,
    agentId: number,
    title: string,
    sprintId: number,
): Promise<number> {
    const taskRes = await request.post('/api/tasks', {
        data: {
            company_id: companyId,
            sprint_id: sprintId,
            title,
            task_type: 'implement',
            agent_id: agentId,
        },
    });
    expect(taskRes.ok(), `create task: ${await taskRes.text()}`).toBeTruthy();
    const task = await taskRes.json();

    const upd = await request.put(`/api/tasks/${task.id}`, {
        data: { status: 'to-do', agent_id: agentId },
    });
    expect(upd.ok(), `update task status: ${await upd.text()}`).toBeTruthy();

    return task.id;
}

/**
 * Extract all tool-result content strings from the LLM messages the mock
 * provider received. This tells us what the agent returned to the LLM after
 * executing each tool call.
 */
function extractToolResults(requests: any[]): string[] {
    const results: string[] = [];
    for (const req of requests) {
        const messages: any[] = (req.body as any)?.messages ?? [];
        for (const msg of messages) {
            if (msg.role === 'tool' && typeof msg.content === 'string') {
                results.push(msg.content);
            }
        }
    }
    return results;
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

test.describe.serial('Agent tools: web_fetch and browser_use', () => {
    const SHORT = 'tool-test';
    let companyId: number;
    let agentId: number;
    let sprintId: number;

    test.beforeAll(async ({ request }) => {
        await request.post('/api/e2e/wipe-db');
        const ws = await setupWorkspace(request, SHORT);
        companyId = ws.companyId;
        agentId = ws.agentId;
        sprintId = ws.sprintId;
    });

    test.beforeEach(async ({ request }) => {
        await resetMockProvider(request);
    });

    // -----------------------------------------------------------------------
    // web_fetch tests
    // -----------------------------------------------------------------------

    test('web_fetch: agent fetches a URL and the content reaches the LLM', async ({ request }) => {
        const html = '<html><body><h1>Hello web_fetch</h1><p>Unique marker: FETCH_OK_42</p></body></html>';
        const server = await startTestServer(html);

        try {
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'wf1',
                        name: 'web_fetch',
                        arguments: { url: server.url },
                    },
                },
                {
                    tool_call: {
                        id: 'ft1',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Fetched successfully.' },
                    },
                },
                { text: 'Task complete.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'web_fetch: fetch test page', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 90_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);

            // At least one tool result must contain both the HTTP status line
            // and the marker text from the served HTML.
            // The marker may arrive with markdown-escaped underscores (FETCH\_OK\_42) depending
            // on how the web_fetch tool renders markdown.
            const fetchResult = toolResults.find((r) => r.includes('FETCH_OK_42') || r.includes('FETCH\\_OK\\_42'));
            expect(fetchResult, `expected web_fetch result with "FETCH_OK_42" in:\n${JSON.stringify(toolResults, null, 2)}`).toBeTruthy();
            expect(fetchResult).toContain('HTTP 200');
        } finally {
            await server.stop();
        }
    });

    test('web_fetch: handles HTTP errors gracefully', async ({ request }) => {
        // Point fetch at a port where nothing is listening.
        const deadPort = await getFreePort(); // guaranteed free since we don't bind it
        const deadUrl = `http://127.0.0.1:${deadPort}/no-such-path`;

        await setScenario(request, [
            {
                tool_call: {
                    id: 'wf2',
                    name: 'web_fetch',
                    arguments: { url: deadUrl },
                },
            },
            {
                tool_call: {
                    id: 'ft2',
                    name: 'finish_task',
                    arguments: { task_status: 'in-review', finish_status: 'Handled error.' },
                },
            },
            { text: 'Done.' },
        ]);

        const taskId = await runTask(request, companyId, agentId, 'web_fetch: connection error', sprintId);
        await waitForTaskStatus(request, taskId, 'in-review', 90_000);

        const reqs = await getMockRequests(request);
        const toolResults = extractToolResults(reqs);

        // The tool must return an error string, not an empty result.
        const errResult = toolResults.find((r) => r.toLowerCase().includes('error'));
        expect(errResult, `expected error message in tool results:\n${JSON.stringify(toolResults, null, 2)}`).toBeTruthy();
    });

    test('web_fetch: respects the 50 KB body limit', async ({ request }) => {
        // Serve a 100 KB payload; the tool should truncate to ~50 KB.
        const bigBody = 'X'.repeat(100_000);
        const server = await startTestServer(`<html><body>${bigBody}</body></html>`);

        try {
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'wf3',
                        name: 'web_fetch',
                        arguments: { url: server.url },
                    },
                },
                {
                    tool_call: {
                        id: 'ft3',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Done.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'web_fetch: large body truncation', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 90_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);
            const fetchResult = toolResults.find((r) => r.startsWith('HTTP'));
            expect(fetchResult, 'expected a web_fetch tool result').toBeTruthy();
            // Result must not exceed ~50 KB + small overhead for the status line.
            expect(fetchResult!.length).toBeLessThan(55_000);
        } finally {
            await server.stop();
        }
    });

    // -----------------------------------------------------------------------
    // browser_use tests
    // -----------------------------------------------------------------------

    test('browser_use navigate: agent loads a page and text content reaches the LLM', async ({ request }) => {
        const html = `
            <html>
              <head><title>Browser Test</title></head>
              <body>
                <h1>Browser Use Works</h1>
                <p id="marker">Unique marker: BROWSER_NAV_99</p>
              </body>
            </html>`;
        const server = await startTestServer(html);

        try {
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'bu1',
                        name: 'browser_use',
                        arguments: { action: 'navigate', url: server.url },
                    },
                },
                {
                    tool_call: {
                        id: 'ft4',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Navigated.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'browser_use: navigate to test page', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 120_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);

            const navResult = toolResults.find((r) => r.includes('BROWSER_NAV_99'));
            expect(
                navResult,
                `expected browser_use result with "BROWSER_NAV_99" in:\n${JSON.stringify(toolResults, null, 2)}`,
            ).toBeTruthy();
            expect(navResult).toContain('Navigated to');
        } finally {
            await server.stop();
        }
    });

    test('browser_use get_text: extracts text from a specific element', async ({ request }) => {
        const html = `
            <html>
              <body>
                <div id="target">Secret value: TEXT_EXTRACT_77</div>
                <div id="other">Ignore this</div>
              </body>
            </html>`;
        const server = await startTestServer(html);

        try {
            await setScenario(request, [
                // Navigate first to load the page
                {
                    tool_call: {
                        id: 'bu2a',
                        name: 'browser_use',
                        arguments: { action: 'navigate', url: server.url },
                    },
                },
                // Then extract specific element text
                {
                    tool_call: {
                        id: 'bu2b',
                        name: 'browser_use',
                        arguments: { action: 'get_text', selector: '#target' },
                    },
                },
                {
                    tool_call: {
                        id: 'ft5',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Extracted.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'browser_use: get_text from selector', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 120_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);

            const extractResult = toolResults.find((r) => r.includes('TEXT_EXTRACT_77'));
            expect(
                extractResult,
                `expected get_text result with "TEXT_EXTRACT_77" in:\n${JSON.stringify(toolResults, null, 2)}`,
            ).toBeTruthy();
        } finally {
            await server.stop();
        }
    });

    test('browser_use execute_js: runs JavaScript and returns result', async ({ request }) => {
        const html = '<html><body><script>window.__testVal = "JS_RESULT_55";</script></body></html>';
        const server = await startTestServer(html);

        try {
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'bu3a',
                        name: 'browser_use',
                        arguments: { action: 'navigate', url: server.url },
                    },
                },
                {
                    tool_call: {
                        id: 'bu3b',
                        name: 'browser_use',
                        arguments: { action: 'execute_js', script: 'window.__testVal' },
                    },
                },
                {
                    tool_call: {
                        id: 'ft6',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'JS executed.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'browser_use: execute_js', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 120_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);

            const jsResult = toolResults.find((r) => r.includes('JS_RESULT_55'));
            expect(
                jsResult,
                `expected execute_js result with "JS_RESULT_55" in:\n${JSON.stringify(toolResults, null, 2)}`,
            ).toBeTruthy();
        } finally {
            await server.stop();
        }
    });

    test('browser_use get_html: returns outer HTML of an element', async ({ request }) => {
        const html = `
            <html>
              <body>
                <section id="sec"><span>HTML_SECTION_33</span></section>
              </body>
            </html>`;
        const server = await startTestServer(html);

        try {
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'bu4a',
                        name: 'browser_use',
                        arguments: { action: 'navigate', url: server.url },
                    },
                },
                {
                    tool_call: {
                        id: 'bu4b',
                        name: 'browser_use',
                        arguments: { action: 'get_html', selector: '#sec' },
                    },
                },
                {
                    tool_call: {
                        id: 'ft7',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Got HTML.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'browser_use: get_html', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 120_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);

            // navigate result also contains HTML_SECTION_33 as text; look specifically for the
            // get_html result which should include both the text and the surrounding HTML tags.
            const htmlResult = toolResults.find((r) => r.includes('HTML_SECTION_33') && r.includes('<section'));
            expect(
                htmlResult,
                `expected get_html result with "<section" and "HTML_SECTION_33" in:\n${JSON.stringify(toolResults, null, 2)}`,
            ).toBeTruthy();
            expect(htmlResult).toContain('<span>');
        } finally {
            await server.stop();
        }
    });

    test('browser_use click + type: interaction sequence on a form page', async ({ request }) => {
        // A page with an input that shows what was typed in a result div.
        const html = `
            <html>
              <body>
                <input id="inp" type="text" oninput="document.getElementById('out').textContent='Typed: '+this.value" />
                <div id="out"></div>
                <button id="btn" onclick="document.getElementById('out').textContent='CLICKED_BTN'">Click me</button>
              </body>
            </html>`;
        const server = await startTestServer(html);

        try {
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'bu5a',
                        name: 'browser_use',
                        arguments: { action: 'navigate', url: server.url },
                    },
                },
                {
                    tool_call: {
                        id: 'bu5b',
                        name: 'browser_use',
                        arguments: { action: 'type', selector: '#inp', text: 'hello_type' },
                    },
                },
                {
                    tool_call: {
                        id: 'bu5c',
                        name: 'browser_use',
                        arguments: { action: 'click', selector: '#btn' },
                    },
                },
                {
                    tool_call: {
                        id: 'bu5d',
                        name: 'browser_use',
                        arguments: { action: 'get_text', selector: '#out' },
                    },
                },
                {
                    tool_call: {
                        id: 'ft8',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Interaction done.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'browser_use: click and type', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 120_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);

            // After clicking the button, #out should read 'CLICKED_BTN'
            const clickResult = toolResults.find((r) => r.includes('CLICKED_BTN'));
            expect(
                clickResult,
                `expected "CLICKED_BTN" in get_text result:\n${JSON.stringify(toolResults, null, 2)}`,
            ).toBeTruthy();
        } finally {
            await server.stop();
        }
    });

    test('browser_use screenshot: returns base64-encoded PNG', async ({ request }) => {
        const html = '<html><body style="background:red"><h1>Screenshot Test</h1></body></html>';
        const server = await startTestServer(html);

        try {
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'bu6a',
                        name: 'browser_use',
                        arguments: { action: 'navigate', url: server.url },
                    },
                },
                {
                    tool_call: {
                        id: 'bu6b',
                        name: 'browser_use',
                        arguments: { action: 'screenshot' },
                    },
                },
                {
                    tool_call: {
                        id: 'ft9',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Screenshot taken.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'browser_use: screenshot', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 120_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);

            // Screenshot result should announce base64 PNG and include data.
            const ssResult = toolResults.find(
                (r) => r.toLowerCase().includes('screenshot') && r.includes('base64'),
            );
            expect(
                ssResult,
                `expected screenshot result with base64 content:\n${JSON.stringify(toolResults, null, 2)}`,
            ).toBeTruthy();
            // Must contain at least some base64 data (PNG magic bytes start with iVBOR...)
            expect(ssResult).toMatch(/iVBOR/);
        } finally {
            await server.stop();
        }
    });

    test('browser_use navigate: handles unknown host gracefully', async ({ request }) => {
        await setScenario(request, [
            {
                tool_call: {
                    id: 'bu7',
                    name: 'browser_use',
                    arguments: { action: 'navigate', url: 'http://this-host-does-not-exist-headcount1.internal/page' },
                },
            },
            {
                tool_call: {
                    id: 'ft10',
                    name: 'finish_task',
                    arguments: { task_status: 'in-review', finish_status: 'Handled error.' },
                },
            },
            { text: 'Done.' },
        ]);

        const taskId = await runTask(request, companyId, agentId, 'browser_use: unknown host error', sprintId);
        await waitForTaskStatus(request, taskId, 'in-review', 120_000);

        const reqs = await getMockRequests(request);
        const toolResults = extractToolResults(reqs);

        // Tool must return an error, not an empty string.
        const errResult = toolResults.find((r) => r.toLowerCase().includes('error'));
        expect(
            errResult,
            `expected an error message in tool results:\n${JSON.stringify(toolResults, null, 2)}`,
        ).toBeTruthy();
    });

    test('web_fetch and browser_use: both tools available in same run', async ({ request }) => {
        const html = '<html><body><p>DUAL_TOOL_TEST</p></body></html>';
        const server = await startTestServer(html);

        try {
            await setScenario(request, [
                // web_fetch first
                {
                    tool_call: {
                        id: 'dual1',
                        name: 'web_fetch',
                        arguments: { url: server.url },
                    },
                },
                // browser_use second
                {
                    tool_call: {
                        id: 'dual2',
                        name: 'browser_use',
                        arguments: { action: 'navigate', url: server.url },
                    },
                },
                {
                    tool_call: {
                        id: 'ft11',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Both tools used.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'dual tool: web_fetch + browser_use', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 120_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);

            // Both tool calls should have produced results containing the marker.
            // web_fetch renders to Markdown by default, which escapes "_" as "\_"
            // (standard CommonMark behavior to avoid accidental emphasis) — strip
            // that escaping before matching so both tools' results count.
            const matchingResults = toolResults.filter((r) => r.replace(/\\_/g, '_').includes('DUAL_TOOL_TEST'));
            expect(matchingResults.length).toBeGreaterThanOrEqual(2);
        } finally {
            await server.stop();
        }
    });

    // -----------------------------------------------------------------------
    // web_fetch to_markdown tests
    // -----------------------------------------------------------------------

    test('web_fetch to_markdown=true (default): returns Markdown instead of raw HTML', async ({ request }) => {
        // Note: markitdown escapes underscores in plain text, so markers use only
        // alphanumeric characters to avoid needing to match the escaped form.
        const html = `
            <html>
              <head><title>Markdown Test Page</title></head>
              <body>
                <h1>Top Heading</h1>
                <p>A paragraph with <strong>bold text</strong> and a
                   <a href="https://example.com">link</a>.</p>
                <ul>
                  <li>Item one</li>
                  <li>Item two</li>
                </ul>
                <p>Marker: MDDEFAULT88</p>
              </body>
            </html>`;
        const server = await startTestServer(html);

        try {
            // to_markdown is omitted — the tool should default to true
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'md1',
                        name: 'web_fetch',
                        arguments: { url: server.url },
                    },
                },
                {
                    tool_call: {
                        id: 'ftmd1',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Got markdown.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'web_fetch: to_markdown default', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 90_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);
            const result = toolResults.find((r) => r.includes('MDDEFAULT88'));
            expect(result, `expected result containing "MDDEFAULT88":\n${JSON.stringify(toolResults)}`).toBeTruthy();

            // Markdown output should use # headings, not <h1> tags.
            expect(result).toContain('# Top Heading');
            expect(result).not.toContain('<h1>');
            // Bold should be ** not <strong>.
            expect(result).toContain('**bold text**');
            expect(result).not.toContain('<strong>');
        } finally {
            await server.stop();
        }
    });

    test('web_fetch to_markdown=true (explicit): returns Markdown', async ({ request }) => {
        const html = `
            <html>
              <body>
                <h2>Section</h2>
                <p>Marker: MDEXPLICIT77</p>
              </body>
            </html>`;
        const server = await startTestServer(html);

        try {
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'md2',
                        name: 'web_fetch',
                        arguments: { url: server.url, to_markdown: true },
                    },
                },
                {
                    tool_call: {
                        id: 'ftmd2',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Got markdown.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'web_fetch: to_markdown explicit true', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 90_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);
            const result = toolResults.find((r) => r.includes('MDEXPLICIT77'));
            expect(result, `expected result containing "MDEXPLICIT77"`).toBeTruthy();
            expect(result).toContain('## Section');
            expect(result).not.toContain('<h2>');
        } finally {
            await server.stop();
        }
    });

    test('web_fetch to_markdown=false: returns raw HTML body', async ({ request }) => {
        const html = `
            <html>
              <body>
                <h1>Raw HTML</h1>
                <p>Marker: RAWHTML66</p>
              </body>
            </html>`;
        const server = await startTestServer(html);

        try {
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'md3',
                        name: 'web_fetch',
                        arguments: { url: server.url, to_markdown: false },
                    },
                },
                {
                    tool_call: {
                        id: 'ftmd3',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Got raw HTML.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'web_fetch: to_markdown=false raw HTML', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 90_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);
            const result = toolResults.find((r) => r.includes('RAWHTML66'));
            expect(result, `expected result containing "RAWHTML66"`).toBeTruthy();
            // Raw mode must preserve HTML tags.
            expect(result).toContain('<h1>');
            expect(result).toContain('<p>');
            expect(result).not.toContain('# Raw HTML');
        } finally {
            await server.stop();
        }
    });

    test('web_fetch to_markdown: plain-text content is returned without crashing', async ({ request }) => {
        // markitdown receives plain text (no HTML tags) and should still return content.
        const server = await startTestServer('Plain text content: NOHTMLTAGS55');

        try {
            await setScenario(request, [
                {
                    tool_call: {
                        id: 'md4',
                        name: 'web_fetch',
                        arguments: { url: server.url },
                    },
                },
                {
                    tool_call: {
                        id: 'ftmd4',
                        name: 'finish_task',
                        arguments: { task_status: 'in-review', finish_status: 'Got content.' },
                    },
                },
                { text: 'Done.' },
            ]);

            const taskId = await runTask(request, companyId, agentId, 'web_fetch: to_markdown plain text', sprintId);
            await waitForTaskStatus(request, taskId, 'in-review', 90_000);

            const reqs = await getMockRequests(request);
            const toolResults = extractToolResults(reqs);
            // Content must be present regardless of conversion success.
            const result = toolResults.find((r) => r.includes('NOHTMLTAGS55') || r.includes('HTTP'));
            expect(result, `expected a result with content:\n${JSON.stringify(toolResults)}`).toBeTruthy();
        } finally {
            await server.stop();
        }
    });
});
