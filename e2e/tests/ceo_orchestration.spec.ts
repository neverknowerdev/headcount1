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
                // CEO turn 4: record the refinement outputs as structured task
                // fields (item lists), separate from the user's original description
                { tool_call: { id: 'c3b', name: 'update_task_details', arguments: {
                    refined_description: 'Show a casual greeting on the home page.',
                    acceptance_criteria: ['Home page shows a casual greeting.', 'Greeting text is configurable.'],
                    test_cases: ['Open home page → casual greeting is visible.'],
                } } },
                // CEO turn 5: delegate implementation to Programmer
                { tool_call: { id: 'c4', name: 'delegate_task', arguments: {
                    title: 'Implement greeting',
                    description: 'Implement the casual greeting per the acceptance criteria.',
                    agent_name: 'Programmer',
                } } },
                // Programmer session: produce an artifact, then finish
                { tool_call: { id: 'p1', name: 'write_artifact', arguments: {
                    filename: 'greeting-report.md',
                    content: '# Greeting implementation\n\nImplemented the casual greeting.',
                    description: 'Implementation report for the greeting feature',
                } } },
                { tool_call: { id: 'p2', name: 'finish_task', arguments: {
                    task_status: 'done', finish_status: 'Casual greeting implemented.',
                } } },
                { text: 'Implementation done.' },
                // CEO tries to finish WITHOUT verification — the engine must
                // reject this (verification gate).
                { tool_call: { id: 'c5', name: 'finish_task', arguments: {
                    task_status: 'in-review', finish_status: 'Attempt to finish before verification.',
                } } },
                // CEO runs verify_implementation, which spawns an independent
                // QA session — the only way to mark spec items as passed.
                { tool_call: { id: 'c6', name: 'verify_implementation', arguments: {
                    notes: 'Greeting implemented; see the artifact report.',
                } } },
                // QA verification session: verdict for every item, then finish.
                { tool_call: { id: 'v1', name: 'report_verification_results', arguments: {
                    results: [
                        { list: 'acceptance_criteria', id: 1, success: true },
                        { list: 'acceptance_criteria', id: 2, success: false, error: 'Config option not implemented yet.' },
                        { list: 'test_cases', id: 1, success: true },
                    ],
                } } },
                { tool_call: { id: 'v2', name: 'finish_task', arguments: {
                    task_status: 'done', finish_status: 'Verification complete: 2 passed, 1 failed.',
                } } },
                { text: 'Verification done.' },
                { tool_call: { id: 'c7', name: 'finish_task', arguments: {
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
        expect(children.length).toBe(3);
        const configs = (children as any[]).map(c => c.agent_config_name);
        expect(configs).toEqual(['QA Lead', 'Programmer', 'QA']);
        for (const child of children) {
            expect(child.parent_run_id).toBe(rootRun.id);
            expect(child.root_run_id).toBe(rootRun.id);
            expect(child.status).toBe('completed');
        }

        // The root run's structured log must record both session boundaries.
        const rootDetails = await (await request.get(`/api/runs/${rootRun.id}`)).json();
        const entryTypes = (rootDetails.log_entries as any[]).map(e => e.type);
        expect(entryTypes.filter(t => t === 'session_started').length).toBe(3);
        expect(entryTypes.filter(t => t === 'session_ended').length).toBe(3);

        // ── Task spec: CEO-generated fields, user input untouched ────────────
        const finalTask = await (await request.get(`/api/tasks/${taskId}`)).json();
        expect(finalTask.description).toBe('Add a greeting to the product.');
        expect(finalTask.refined_description).toBe('Show a casual greeting on the home page.');

        // Criteria and test cases are structured item lists with per-item
        // verdicts recorded during the verification stage.
        const acItems = JSON.parse(finalTask.acceptance_criteria);
        expect(acItems).toEqual([
            { id: 1, text: 'Home page shows a casual greeting.', status: 'passed' },
            { id: 2, text: 'Greeting text is configurable.', status: 'failed', note: 'Config option not implemented yet.' },
        ]);
        const tcItems = JSON.parse(finalTask.test_cases);
        expect(tcItems).toEqual([
            { id: 1, text: 'Open home page → casual greeting is visible.', status: 'passed' },
        ]);

        // The premature finish_task (before verification) must have been
        // rejected by the engine's verification gate.
        const rootEntries = rootDetails.log_entries as any[];
        const gateError = rootEntries.find(e =>
            e.type === 'tool_response' && typeof e.content === 'string' && e.content.includes('unverified'));
        expect(gateError, 'finish_task before verify_implementation should be rejected').toBeTruthy();

        // ── Artifacts: produced by the Programmer, listed with metadata ──────
        const artifacts = await (await request.get(`/api/tasks/${taskId}/artifacts`)).json();
        expect(artifacts.length).toBe(1);
        const artifact = artifacts[0];
        expect(artifact.filename).toBe('greeting-report.md');
        expect(artifact.description).toBe('Implementation report for the greeting feature');
        // One acceptance criterion failed, so the artifacts are NOT marked verified.
        expect(artifact.is_verified).toBeFalsy();

        // Download endpoints: single file and the whole-task zip.
        const dl = await request.get(`/api/artifacts/${artifact.id}/download`);
        expect(dl.ok()).toBeTruthy();
        expect((await dl.text())).toContain('Implemented the casual greeting.');
        const zipRes = await request.get(`/api/tasks/${taskId}/artifacts/download`);
        expect(zipRes.ok()).toBeTruthy();
        expect(zipRes.headers()['content-type']).toContain('application/zip');

        // ── Subtasks: two delegations plus the QA verification session ───────
        const allTasks = await (await request.get(`/api/tasks?company_id=${companyId}`)).json();
        const subtasks = (allTasks as any[]).filter(t => t.parent_id === taskId);
        expect(subtasks.length).toBe(3);
        for (const st of subtasks) {
            expect(st.status).toBe('done');
        }
        // Delegated subtasks carry no raw user input — the orchestrator's
        // instructions live in refined_description.
        const implTask = subtasks.find((t: any) => t.title === 'Implement greeting');
        expect(implTask.description).toBe('');
        expect(implTask.refined_description).toBe('Implement the casual greeting per the acceptance criteria.');
        const verifyTask = subtasks.find((t: any) => t.title.startsWith('Verify:'));
        expect(verifyTask, 'a QA verification subtask should exist').toBeTruthy();
        expect(verifyTask.agent_config_name).toBe('QA');

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
        await expect(sessionBlocks).toHaveCount(3);
        await expect(sessionBlocks.first()).toContainText('QA Lead');
        await expect(sessionBlocks.first()).toContainText('Define acceptance criteria');

        // Expand the first session and verify the nested log loads.
        await sessionBlocks.first().getByRole('button').first().click();
        await expect(sessionBlocks.first().getByText('Execution Log')).toBeVisible({ timeout: 15_000 });

        // Expanding the token bar reveals the per-agent breakdown across sessions.
        // The expanded session block renders a nested viewer with its own bar;
        // the first token bar on the page belongs to the root run.
        await page.locator('button[title="Click for detailed breakdown"]').first().click();
        const agentTokenStats = page.getByTestId('agent-token-stats');
        await expect(agentTokenStats).toBeVisible();
        await expect(agentTokenStats).toContainText('CEO');
        await expect(agentTokenStats).toContainText('QA Lead');
        await expect(agentTokenStats).toContainText('Programmer');
        await expect(agentTokenStats).toContainText('QA');

        // Run Logs list: only the main session is a top-level card. Its
        // sub-sessions are nested one level down and stay collapsed until the
        // card itself, then the individual session block, is maximized.
        await page.goto('/companies/ceo-co/runs');
        await expect(page.getByRole('heading', { name: 'Run Logs' })).toBeVisible();
        const rootCards = page.getByTestId('root-run-card');
        await expect(rootCards).toHaveCount(1);
        await expect(rootCards.first()).toContainText('3 sessions');

        // Sub-session logs stay hidden until the root card itself is maximized
        // (native <details> collapses its content).
        await expect(rootCards.first().getByTestId('session-block').first()).not.toBeVisible();
        await rootCards.first().locator('summary').click();
        const listSessionBlocks = rootCards.first().getByTestId('session-block');
        await expect(listSessionBlocks).toHaveCount(3);
        await expect(listSessionBlocks.first()).toBeVisible();
        await expect(listSessionBlocks.first()).toContainText('QA Lead');

        // Each nested session itself stays collapsed (not even mounted) until
        // it is individually maximized.
        await expect(listSessionBlocks.first().getByText('Execution Log')).toHaveCount(0);
        await listSessionBlocks.first().getByRole('button').first().click();
        await expect(listSessionBlocks.first().getByText('Execution Log')).toBeVisible({ timeout: 15_000 });

        // Task view separates the user input from the CEO-generated spec.
        await page.goto(`/companies/ceo-co/tasks/${taskId}`);
        await expect(page.locator('textarea')).toHaveValue('Add a greeting to the product.');
        const ceoSpec = page.getByTestId('ceo-spec');
        await expect(ceoSpec).toBeVisible();
        await expect(ceoSpec.getByText('Generated by CEO')).toHaveCount(3);

        // The refined description is shown expanded, right next to the user input.
        await expect(ceoSpec.getByText('Refined Description')).toBeVisible();
        await expect(ceoSpec.getByText('Show a casual greeting on the home page.')).toBeVisible();

        // Acceptance criteria and test cases are minimized by default, with a
        // verification progress badge in the header…
        await expect(ceoSpec.getByText('Acceptance Criteria')).toBeVisible();
        await expect(ceoSpec.getByText('Test Cases')).toBeVisible();
        await expect(ceoSpec.getByText('1/2 passed, 1 failed')).toBeVisible();
        await expect(ceoSpec.getByText('1/1 passed')).toBeVisible();
        await expect(ceoSpec.getByText('Home page shows a casual greeting.')).toHaveCount(0);

        // …and expand into a per-item checklist with verdicts.
        await ceoSpec.getByRole('button', { name: /Acceptance Criteria/ }).click();
        await expect(ceoSpec.getByText('Home page shows a casual greeting.')).toBeVisible();
        await expect(ceoSpec.getByText('Greeting text is configurable.')).toBeVisible();
        await expect(ceoSpec.getByText('Config option not implemented yet.')).toBeVisible();
        await expect(ceoSpec.getByText('✅')).toHaveCount(1);
        await expect(ceoSpec.getByText('❌')).toHaveCount(1);
        await ceoSpec.getByRole('button', { name: /Test Cases/ }).click();
        await expect(ceoSpec.getByText(/casual greeting is visible/)).toBeVisible();

        // Artifacts block at task-description level: collapsed by default,
        // expandable per artifact, with download links.
        const artifactsBlock = page.getByTestId('artifacts-block');
        await expect(artifactsBlock).toBeVisible();
        await expect(artifactsBlock).toContainText('1 artifact');
        await expect(artifactsBlock).toContainText('Download all');
        await expect(artifactsBlock.getByText('greeting-report.md')).toBeHidden();
        await artifactsBlock.getByRole('button', { name: /1 artifact/ }).click();
        await expect(artifactsBlock.getByText('greeting-report.md')).toBeVisible();
        await expect(artifactsBlock.getByText('Implementation report for the greeting feature')).toBeVisible();
        // Expand the artifact row to see its content inline.
        await artifactsBlock.getByRole('button', { name: /greeting-report\.md/ }).click();
        await expect(artifactsBlock.getByText('Implemented the casual greeting.')).toBeVisible();
    });

    test('re-running a subtask or child session restarts the main session', async ({ page, request }) => {
        // Reset the mock to default mode: first call answers finish_task(in-review).
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });

        const allTasks = await (await request.get(`/api/tasks?company_id=${companyId}`)).json();
        const subtask = (allTasks as any[]).find(t => t.parent_id === taskId);
        expect(subtask).toBeTruthy();

        const mainRunsBefore = await (await request.get(`/api/tasks/${taskId}/runs`)).json();
        const subRunsBefore = await (await request.get(`/api/tasks/${subtask.id}/runs`)).json();

        // Child session runs are not re-runnable entry points.
        const childRun = await (await request.get(`/api/runs/${subRunsBefore[0].id}`)).json();
        expect(childRun.is_latest).toBeFalsy();

        // Re-running the SUBTASK must restart the parent's main session.
        const rerunRes = await request.post(`/api/tasks/${subtask.id}/rerun`);
        expect(rerunRes.ok()).toBeTruthy();
        expect((await rerunRes.json()).task_id).toBe(taskId);

        await waitForTaskStatus(request, taskId, 'in-review', 90_000);
        await expect.poll(async () => {
            const runs = await (await request.get(`/api/tasks/${taskId}/runs`)).json();
            return runs.length;
        }, { timeout: 30_000, message: 'main task should get a new main-session run' }).toBe(mainRunsBefore.length + 1);

        // No orphan sub-session run was spawned on the subtask itself.
        const subRunsAfter = await (await request.get(`/api/tasks/${subtask.id}/runs`)).json();
        expect(subRunsAfter.length).toBe(subRunsBefore.length);

        // The subtask view labels itself and its runs as delegated sub-sessions.
        await page.goto(`/companies/ceo-co/tasks/${subtask.id}`);
        await expect(page.getByTestId('subtask-banner')).toBeVisible();
        await expect(page.getByText('sub-session').first()).toBeVisible();
    });

    test('agents page lists built-in agents, minimized by default', async ({ page }) => {
        await page.goto('/companies/ceo-co/agents');
        const builtin = page.getByTestId('builtin-agents');
        await expect(builtin).toBeVisible();
        await expect(builtin).toContainText('Built-in agents');
        // Minimized by default: the cards are not rendered until expanded.
        await expect(builtin.getByText('Chief Executive Officer — orchestrates task execution through delegation')).toBeHidden();
        await builtin.getByRole('button').first().click();
        await expect(builtin.getByText('Chief Executive Officer — orchestrates task execution through delegation')).toBeVisible();
        await expect(builtin.getByText('QA Lead — defines acceptance criteria and test cases')).toBeVisible();
        await expect(builtin.getByText('Social media marketing — posts, announcements, and content plans')).toBeVisible();
    });
});

async function postJSON(request: APIRequestContext, url: string, data: unknown): Promise<any> {
    const res = await request.post(url, { data });
    if (!res.ok()) {
        throw new Error(`POST ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}
