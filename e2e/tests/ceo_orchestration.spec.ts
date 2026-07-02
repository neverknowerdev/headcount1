import { test, expect, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';

const env = loadE2EEnv();

const QUESTION = 'Should the greeting be formal or casual?';
const HUMAN_REPLY = 'Casual, please.';
const STATUS_LINE = 'Refinement: clarifying requirements';

/**
 * Full CEO orchestration flow, driven end-to-end through the real engine with
 * a scripted mock LLM provider:
 *
 *   CEO (root session)
 *     1. report_status                 -> visible progress line on the run
 *     2. ask_human                     -> ask_user comment, waits for reply
 *     3. delegate_task -> QA Lead      -> nested session (acceptance criteria)
 *     4. delegate_task -> Programmer   -> nested session (implementation)
 *     5. finish_task(in-review)        -> final task status
 *
 * Delegation is synchronous, so the global order of chat-completion requests
 * is deterministic and one scenario list drives all three sessions.
 */
test.describe.serial('CEO orchestration flow', () => {
    let companyId: number;
    let taskId: number;
    let providerId: number;

    const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');

    const cleanFilesystem = () => {
        for (const subDir of ['data/ceo-co', 'companies/ceo-co', 'workspace/ceo-co', 'data/runs/ceo-co']) {
            const fullPath = path.join(paperclipBase, subDir);
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }
        // Remove the provider file too: leftover entity files get re-imported by
        // the filesystem sync tests and collide with their freshly created ids.
        if (providerId) {
            const providerFile = path.join(paperclipBase, 'data', 'llm-providers', `${providerId}.json`);
            if (fs.existsSync(providerFile)) fs.rmSync(providerFile, { force: true });
        }
    };

    test.beforeAll(async ({ request }) => {
        cleanFilesystem();
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
    });

    test.afterAll(async ({ request }) => {
        // Leave no filesystem or DB state behind for the specs that follow.
        cleanFilesystem();
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
    });

    test('task is refined, delegated, verified and finished by the CEO', async ({ page, request }) => {
        // ── Setup: provider, company, agent, sprint, task (all via API) ──────
        const provider = await postJSON(request, '/api/providers', {
            name: 'e2e-mock',
            base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key',
            provider_type: 'openai',
            default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model',
        });
        providerId = provider.id;
        const company = await postJSON(request, '/api/companies', {
            name: 'CEO Co', short_name: 'ceo-co', color: '#4f46e5',
        });
        companyId = company.id;
        const agent = await postJSON(request, '/api/agents', {
            company_id: companyId,
            name: 'Orchestrator',
            system_prompt: 'You orchestrate.',
            model: 'e2e-mock-model',
            provider_id: provider.id,
        });
        const sprint = await postJSON(request, '/api/sprints', {
            company_id: companyId, name: 'CEO Sprint',
        });
        const task = await postJSON(request, '/api/tasks', {
            company_id: companyId,
            sprint_id: sprint.id,
            agent_id: agent.id,
            title: 'Build greeting feature',
            description: 'Add a greeting to the product.',
            task_type: 'implement',
        });
        taskId = task.id;

        // ── Script the LLM: one global sequence across all sessions ─────────
        const scenario = {
            entries: [
                // CEO turn 1: report progress
                { tool_call: { id: 'c1', name: 'report_status', arguments: { status: STATUS_LINE } } },
                // CEO turn 2: refinement question to the human
                { tool_call: { id: 'c2', name: 'ask_human', arguments: { question: QUESTION } } },
                // CEO turn 3: delegate acceptance criteria to QA Lead
                { tool_call: { id: 'c3', name: 'delegate_task', arguments: {
                    title: 'Define acceptance criteria',
                    description: 'Write acceptance criteria for the greeting feature. Greeting must be casual.',
                    agent_name: 'QA Lead',
                } } },
                // QA Lead session
                { tool_call: { id: 'q1', name: 'finish_task', arguments: {
                    task_status: 'done', finish_status: 'Acceptance criteria defined: casual greeting shown on home page.',
                } } },
                { text: 'Acceptance criteria are ready.' },
                // CEO turn 4: delegate implementation to Programmer
                { tool_call: { id: 'c4', name: 'delegate_task', arguments: {
                    title: 'Implement greeting',
                    description: 'Implement the casual greeting per the acceptance criteria.',
                    agent_name: 'Programmer',
                } } },
                // Programmer session
                { tool_call: { id: 'p1', name: 'finish_task', arguments: {
                    task_status: 'done', finish_status: 'Casual greeting implemented.',
                } } },
                { text: 'Implementation done.' },
                // CEO turn 5: wrap up
                { tool_call: { id: 'c5', name: 'finish_task', arguments: {
                    task_status: 'in-review', finish_status: 'Greeting feature delegated, implemented and verified.',
                } } },
                { text: 'Task complete.' },
            ],
        };
        const scRes = await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(scenario),
        });
        expect(scRes.ok).toBeTruthy();

        // ── Kick off: moving the task to "to-do" triggers the engine ─────────
        const upd = await request.put(`/api/tasks/${taskId}`, { data: { status: 'to-do' } });
        expect(upd.ok()).toBeTruthy();

        // ── Refinement: the CEO asks the human and waits ─────────────────────
        await expect.poll(async () => {
            const res = await request.get(`/api/comments?task_id=${taskId}`);
            if (!res.ok()) return false;
            const comments = await res.json();
            return (comments as any[]).some(c => c.comment_type === 'ask_user' && c.content === QUESTION);
        }, { timeout: 60_000, message: 'ask_user comment should appear' }).toBeTruthy();

        // While waiting for the human, the task must still be in progress.
        const midTask = await (await request.get(`/api/tasks/${taskId}`)).json();
        expect(midTask.status).toBe('in-progress');

        // Reply as the human — this unblocks ask_human inside the same run.
        await postJSON(request, '/api/comments', {
            task_id: taskId, author_type: 'human', content: HUMAN_REPLY,
        });

        // ── Completion: CEO finishes the task after both delegations ─────────
        await waitForTaskStatus(request, taskId, 'in-review', 90_000);

        // ── Runs: one root (CEO) session with two nested child sessions ──────
        // finish_task flips the task status before the run itself wraps up, so
        // poll until the root run reaches a terminal status.
        await expect.poll(async () => {
            const res = await request.get(`/api/tasks/${taskId}/runs`);
            if (!res.ok()) return '';
            const rs = await res.json();
            return rs.length === 1 ? rs[0].status : '';
        }, { timeout: 30_000, message: 'root run should complete' }).toBe('completed');
        const runs = await (await request.get(`/api/tasks/${taskId}/runs`)).json();
        expect(runs.length).toBe(1);
        const rootRun = runs[0];
        expect(rootRun.parent_run_id).toBeFalsy();
        expect(rootRun.root_run_id).toBe(rootRun.id);
        expect(rootRun.agent_config_name).toBe('CEO');
        expect(rootRun.status).toBe('completed');
        expect(rootRun.current_status).toBe(STATUS_LINE);
        expect(rootRun.result_description).toBe('Greeting feature delegated, implemented and verified.');

        const children = await (await request.get(`/api/runs/${rootRun.id}/children`)).json();
        expect(children.length).toBe(2);
        const configs = (children as any[]).map(c => c.agent_config_name);
        expect(configs).toEqual(['QA Lead', 'Programmer']);
        for (const child of children) {
            expect(child.parent_run_id).toBe(rootRun.id);
            expect(child.root_run_id).toBe(rootRun.id);
            expect(child.status).toBe('completed');
        }

        // The root run's structured log must record both session boundaries.
        const rootDetails = await (await request.get(`/api/runs/${rootRun.id}`)).json();
        const entryTypes = (rootDetails.log_entries as any[]).map(e => e.type);
        expect(entryTypes.filter(t => t === 'session_started').length).toBe(2);
        expect(entryTypes.filter(t => t === 'session_ended').length).toBe(2);

        // ── Subtasks: both delegations created and finished their tasks ──────
        const allTasks = await (await request.get(`/api/tasks?company_id=${companyId}`)).json();
        const subtasks = (allTasks as any[]).filter(t => t.parent_id === taskId);
        expect(subtasks.length).toBe(2);
        for (const st of subtasks) {
            expect(st.status).toBe('done');
        }

        // ── Filesystem: logs grouped by main run id, one file per session ────
        const basePath = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');
        const runDir = path.join(basePath, 'data', 'ceo-co', 'logs', String(taskId), `run-${rootRun.id}`);
        const mainLog = path.join(runDir, 'main.log');
        expect(fs.existsSync(mainLog)).toBeTruthy();
        const mainContent = fs.readFileSync(mainLog, 'utf8');
        expect(mainContent).toContain('Session Started');
        expect(mainContent).toContain('Session Ended');
        for (const child of children) {
            const sessionLog = path.join(runDir, `session-${child.id}.log`);
            expect(fs.existsSync(sessionLog)).toBeTruthy();
            const sessionContent = fs.readFileSync(sessionLog, 'utf8');
            expect(sessionContent).toContain('LLM Request');
        }

        // ── UI: Run Log view shows the main flow with expandable sessions ────
        await page.goto(`/companies/ceo-co/run-logs/${rootRun.id}`);
        await expect(page.getByRole('heading', { name: `Run #${rootRun.id} Details` })).toBeVisible({ timeout: 15_000 });
        await expect(page.getByTestId('run-current-status')).toHaveText(STATUS_LINE);

        const sessionBlocks = page.getByTestId('session-block');
        await expect(sessionBlocks).toHaveCount(2);
        await expect(sessionBlocks.first()).toContainText('QA Lead');
        await expect(sessionBlocks.first()).toContainText('Define acceptance criteria');

        // Expand the first session and verify the nested log loads.
        await sessionBlocks.first().getByRole('button').first().click();
        await expect(sessionBlocks.first().getByText('Execution Log')).toBeVisible({ timeout: 15_000 });

        // Run Logs list groups the child sessions under the root run.
        await page.goto('/companies/ceo-co/runs');
        await expect(page.getByRole('heading', { name: 'Run Logs' })).toBeVisible();
        await expect(page.getByText('2 sessions')).toBeVisible();
        // Child session rows are inside the collapsed run card — expand it.
        await page.locator('summary', { hasText: `Run #${rootRun.id}` }).click();
        await expect(page.getByText(/↳ Session #\d+ · QA Lead/)).toBeVisible();
    });
});

async function postJSON(request: APIRequestContext, url: string, data: unknown): Promise<any> {
    const res = await request.post(url, { data });
    if (!res.ok()) {
        throw new Error(`POST ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}
