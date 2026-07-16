import { test, expect, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';

const env = loadE2EEnv();

const QUESTION = 'Should the greeting be formal or casual?';
const HUMAN_REPLY = 'Casual, please.';
const STATUS_LINE = 'Planning: reviewing the request';
const OWNER_QUESTION = 'Should the greeting appear on the home page only, or on every page?';
const OWNER_ANSWER = 'Home page only.';

/**
 * Full orchestration flow, driven end-to-end through the real engine with a
 * scripted mock LLM provider, across the three-level hierarchy:
 *
 *   CEO (root session)
 *     1. report_status                    -> visible progress line on the run
 *     2. ask_human                        -> ask_user comment, waits for reply
 *     3. create_subtask -> CTO            -> nested session
 *          CTO: ask_task_owner            -> pauses; CEO gets the question
 *     4. answer_subtask_question          -> CTO resumes
 *          CTO: create_subtask -> Coder   -> nested session (writes artifact)
 *          CTO: create_subtask -> QA      -> nested session (verifies)
 *          CTO: finish_task(done)
 *     5. finish_task(in-review)           -> final task status
 *
 * Delegation blocks the owner session, so the global order of chat-completion
 * requests is deterministic and one scenario list drives all four sessions.
 */
test.describe.serial('CEO orchestration flow', () => {
    let companyId: number;
    let taskId: number;
    let providerId: number;

    const headcount1Base = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');

    const cleanFilesystem = () => {
        for (const subDir of ['data/ceo-co', 'companies/ceo-co', 'workspace/ceo-co', 'data/runs/ceo-co']) {
            const fullPath = path.join(headcount1Base, subDir);
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }
        // Remove the provider file too: leftover entity files get re-imported by
        // the filesystem sync tests and collide with their freshly created ids.
        if (providerId) {
            const providerFile = path.join(headcount1Base, 'data', 'llm-providers', `${providerId}.json`);
            if (fs.existsSync(providerFile)) fs.rmSync(providerFile, { force: true });
        }
    };

    test.beforeAll(async ({ request }) => {
        cleanFilesystem();
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
    });

    test.afterAll(async ({ request }) => {
        // Leave no filesystem, DB or settings state behind for the specs that
        // follow — wipe-db also re-seeds the built-in Utility/Memory
        // Management model groups back to their default state.
        cleanFilesystem();
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
    });

    test('task is delegated CEO → CTO → Coder/QA with a question round-trip', async ({ page, request }) => {
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

        // The Default Model for ask_artifact's one-shot reader call: point it
        // directly at the mock provider so the reader call is deterministic.
        const askArtifactUpd = await request.put('/api/default-model-settings/ask_artifact', {
            data: { provider_id: provider.id, model: 'e2e-mock-model' },
        });
        if (!askArtifactUpd.ok()) {
            throw new Error(`PUT /api/default-model-settings/ask_artifact failed (${askArtifactUpd.status()}): ${await askArtifactUpd.text()}`);
        }
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
                // CEO turn 2: question to the human
                { tool_call: { id: 'c2', name: 'ask_human', arguments: { question: QUESTION } } },
                // CEO turn 3: delegate the whole technical job to the CTO
                { tool_call: { id: 'c3', name: 'create_subtask', arguments: {
                    title: 'Implement greeting feature',
                    description: 'Implement a casual greeting per the user decision. Verify it before reporting back.',
                    agent_name: 'CTO',
                } } },
                // CTO turn 1: ask the CEO (task owner) a clarifying question —
                // this pauses the CTO session and returns the question to the CEO.
                { tool_call: { id: 't1', name: 'ask_task_owner', arguments: { question: OWNER_QUESTION } } },
                // CEO turn 4: answer the pending question (single pending → no id needed)
                { tool_call: { id: 'c4', name: 'answer_subtask_question', arguments: { answer: OWNER_ANSWER } } },
                // CTO turn 2 (resumed): delegate implementation to the Coder
                { tool_call: { id: 't2', name: 'create_subtask', arguments: {
                    title: 'Write greeting code',
                    description: 'Add a casual greeting to the home page.',
                    agent_name: 'Coder',
                } } },
                // Coder session: produce an artifact, then finish (terminal).
                { tool_call: { id: 'p1', name: 'write_artifact', arguments: {
                    filename: 'greeting-report.md',
                    content: '# Greeting implementation\n\nImplemented the casual greeting.',
                    description: 'Implementation report for the greeting feature',
                } } },
                { tool_call: { id: 'p2', name: 'finish_task', arguments: {
                    task_status: 'done',
                    finish_status: 'Casual greeting implemented.',
                    result_details: 'Implemented the casual greeting on the home page; see greeting-report.md.',
                } } },
                // CTO turn 3: delegate verification to QA
                { tool_call: { id: 't3', name: 'create_subtask', arguments: {
                    title: 'Verify greeting',
                    description: 'Verify the casual greeting shows on the home page. Read greeting-report.md for what was built.',
                    agent_name: 'QA',
                } } },
                // QA session: verdict, then finish (terminal).
                { tool_call: { id: 'q1', name: 'finish_task', arguments: {
                    task_status: 'done',
                    finish_status: 'Verified: casual greeting shows on the home page.',
                } } },
                // CTO turn 4: wrap up
                { tool_call: { id: 't4', name: 'finish_task', arguments: {
                    task_status: 'done',
                    finish_status: 'Greeting implemented and verified.',
                    result_details: 'Coder implemented the greeting (greeting-report.md), QA verified it on the home page.',
                } } },
                // CEO turn 5: spot-check the deliverable without reading it —
                // ask_artifact runs a separate one-shot reader call.
                { tool_call: { id: 'c5', name: 'ask_artifact', arguments: {
                    filename: 'greeting-report.md',
                    question: 'Does the report confirm the greeting is casual?',
                } } },
                // Consumed by the one-shot reader call (Utility model group).
                { text: 'Yes — the report states the casual greeting was implemented.' },
                // CEO turn 6: plan follow-up work as a separate TOP-LEVEL task
                // on the board (backlog — nothing executes).
                { tool_call: { id: 'c6', name: 'create_task', arguments: {
                    title: 'Announce the greeting feature',
                    description: 'Prepare and publish an announcement once the greeting ships.',
                    priority: 'High',
                } } },
                // CEO turn 7: finish the root task
                { tool_call: { id: 'c7', name: 'finish_task', arguments: {
                    task_status: 'in-review',
                    finish_status: 'Greeting feature delegated, implemented and verified.',
                } } },
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

        // ── The CEO asks the human and waits ─────────────────────────────────
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

        // ── Completion: CEO finishes the task after the delegation tree ──────
        await waitForTaskStatus(request, taskId, 'in-review', 90_000);

        // ── Runs: one root (CEO) session with a nested CTO session, which in
        //    turn ran Coder and QA sessions ─────────────────────────────────────
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

        // Direct children of the root: exactly one CTO session.
        const rootChildren = await (await request.get(`/api/runs/${rootRun.id}/children`)).json();
        expect(rootChildren.length).toBe(1);
        const ctoRun = rootChildren[0];
        expect(ctoRun.agent_config_name).toBe('CTO');
        expect(ctoRun.parent_run_id).toBe(rootRun.id);
        expect(ctoRun.root_run_id).toBe(rootRun.id);
        expect(ctoRun.status).toBe('completed');

        // Direct children of the CTO session: Coder then QA.
        const ctoChildren = await (await request.get(`/api/runs/${ctoRun.id}/children`)).json();
        expect((ctoChildren as any[]).map(c => c.agent_config_name)).toEqual(['Coder', 'QA']);
        for (const child of ctoChildren) {
            expect(child.parent_run_id).toBe(ctoRun.id);
            expect(child.root_run_id).toBe(rootRun.id);
            expect(child.status).toBe('completed');
        }

        // ?deep=true returns the whole tree from the root.
        const deepChildren = await (await request.get(`/api/runs/${rootRun.id}/children?deep=true`)).json();
        expect((deepChildren as any[]).map(c => c.agent_config_name).sort()).toEqual(['CTO', 'Coder', 'QA']);

        // The root run's structured log records its (single) session boundary;
        // the CTO run's log records its two.
        const rootDetails = await (await request.get(`/api/runs/${rootRun.id}`)).json();
        const rootEntryTypes = (rootDetails.log_entries as any[]).map(e => e.type);
        expect(rootEntryTypes.filter(t => t === 'session_started').length).toBe(1);
        expect(rootEntryTypes.filter(t => t === 'session_ended').length).toBe(1);
        const ctoDetails = await (await request.get(`/api/runs/${ctoRun.id}`)).json();
        const ctoEntryTypes = (ctoDetails.log_entries as any[]).map(e => e.type);
        expect(ctoEntryTypes.filter(t => t === 'session_started').length).toBe(2);
        expect(ctoEntryTypes.filter(t => t === 'session_ended').length).toBe(2);

        // ── Question round-trip: create_subtask returned the CTO's question,
        //    the CEO answered, and both are recorded on the CTO subtask ───────
        const subtasks = await (await request.get(`/api/tasks?company_id=${companyId}&parent_id=${taskId}`)).json();
        expect(subtasks.length).toBe(1);
        const ctoTask = subtasks[0];
        expect(ctoTask.agent_config_name).toBe('CTO');
        expect(ctoTask.status).toBe('done');
        // Delegated subtasks carry no raw user input — the owner's
        // instructions live in refined_description.
        expect(ctoTask.description).toBe('');
        expect(ctoTask.refined_description).toContain('Implement a casual greeting');

        const ctoComments = await (await request.get(`/api/comments?task_id=${ctoTask.id}`)).json();
        expect((ctoComments as any[]).some(c => c.comment_type === 'ask_owner' && c.content === OWNER_QUESTION),
            'ask_owner comment should be recorded on the subtask').toBeTruthy();
        expect((ctoComments as any[]).some(c => c.comment_type === 'owner_answer' && c.content === OWNER_ANSWER),
            'owner_answer comment should be recorded on the subtask').toBeTruthy();

        // The CEO saw the question as the create_subtask tool result.
        const mockLog = await (await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const toolResults: string[] = [];
        for (const req of mockLog.requests as any[]) {
            for (const msg of ((req.body as any)?.messages ?? [])) {
                if (msg.role === 'tool' && typeof msg.content === 'string') toolResults.push(msg.content);
            }
        }
        expect(toolResults.some(c => c.includes(OWNER_QUESTION) && c.includes('answer_subtask_question')),
            'CEO should have received the CTO question as a tool result').toBeTruthy();
        expect(toolResults.some(c => c.includes(OWNER_ANSWER)),
            'CTO should have received the CEO answer as a tool result').toBeTruthy();
        // The final answer_subtask_question result carries the CTO's handoff.
        expect(toolResults.some(c => c.includes('Greeting implemented and verified.') && c.includes('Coder implemented the greeting')),
            'CEO should have received the CTO final result with details').toBeTruthy();
        // ask_artifact returned only the reader's short answer, never the raw content.
        expect(toolResults.some(c => c.includes('Answer about "greeting-report.md"') && c.includes('casual greeting was implemented')),
            'CEO should have received the ask_artifact answer').toBeTruthy();

        // ── create_task: a follow-up task landed on the board as a TOP-LEVEL
        //    task in the backlog, with its own ref key and no runs ───────────
        const boardTasks = await (await request.get(`/api/tasks?company_id=${companyId}`)).json();
        const planned = (boardTasks as any[]).find(t => t.title === 'Announce the greeting feature');
        expect(planned, 'create_task should place a task on the board').toBeTruthy();
        expect(planned.parent_id).toBeFalsy();
        expect(planned.status).toBe('backlog');
        expect(planned.priority).toBe('High');
        expect(planned.ref_key).toMatch(/^[A-Z-]+-\d+$/);
        const plannedRuns = await (await request.get(`/api/tasks/${planned.id}/runs`)).json();
        expect(plannedRuns.length).toBe(0);

        // ── Task spec: user input untouched, no forced refinement fields ─────
        const finalTask = await (await request.get(`/api/tasks/${taskId}`)).json();
        expect(finalTask.description).toBe('Add a greeting to the product.');
        expect(finalTask.refined_description).toBeFalsy();
        expect(finalTask.acceptance_criteria).toBeFalsy();

        // ── Artifacts: produced by the Coder, listed with metadata ───────────
        const artifacts = await (await request.get(`/api/tasks/${taskId}/artifacts`)).json();
        expect(artifacts.length).toBe(1);
        const artifact = artifacts[0];
        expect(artifact.filename).toBe('greeting-report.md');
        expect(artifact.description).toBe('Implementation report for the greeting feature');

        // Download endpoints: single file and the whole-task zip.
        const dl = await request.get(`/api/artifacts/${artifact.id}/download`);
        expect(dl.ok()).toBeTruthy();
        expect((await dl.text())).toContain('Implemented the casual greeting.');
        const zipRes = await request.get(`/api/tasks/${taskId}/artifacts/download`);
        expect(zipRes.ok()).toBeTruthy();
        expect(zipRes.headers()['content-type']).toContain('application/zip');

        // ── Subtasks of the CTO task: Coder and QA, both done ────────────────
        const ctoSubtasks = await (await request.get(`/api/tasks?company_id=${companyId}&parent_id=${ctoTask.id}`)).json();
        expect(ctoSubtasks.length).toBe(2);
        for (const st of ctoSubtasks) {
            expect(st.status).toBe('done');
        }
        const coderTask = (ctoSubtasks as any[]).find(t => t.title === 'Write greeting code');
        expect(coderTask.agent_config_name).toBe('Coder');
        expect(coderTask.refined_description).toBe('Add a casual greeting to the home page.');
        const qaTask = (ctoSubtasks as any[]).find(t => t.title === 'Verify greeting');
        expect(qaTask.agent_config_name).toBe('QA');

        // ── Filesystem: logs grouped by main run id, one file per session ────
        const basePath = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');
        const runDir = path.join(basePath, 'data', 'ceo-co', 'logs', String(taskId), `run-${rootRun.id}`);
        const mainLog = path.join(runDir, 'main.jsonl');
        expect(fs.existsSync(mainLog)).toBeTruthy();
        const mainEntries = fs.readFileSync(mainLog, 'utf8').split('\n').filter(l => l.trim()).map(l => JSON.parse(l));
        expect(mainEntries.some(e => e.type === 'session_started')).toBeTruthy();
        expect(mainEntries.some(e => e.type === 'session_ended')).toBeTruthy();
        for (const child of [ctoRun, ...ctoChildren]) {
            const sessionLog = path.join(runDir, `session-${child.id}.jsonl`);
            expect(fs.existsSync(sessionLog)).toBeTruthy();
            const sessionEntries = fs.readFileSync(sessionLog, 'utf8').split('\n').filter(l => l.trim()).map(l => JSON.parse(l));
            expect(sessionEntries.some(e => e.type === 'request')).toBeTruthy();
        }

        // The ask_artifact reader exchange got its own log file in the run
        // folder, holding the full prompt (artifact content) and the answer.
        const askLogs = fs.readdirSync(runDir).filter(f => f.startsWith(`ask-artifact-${rootRun.id}-`) && f.endsWith('.log'));
        expect(askLogs.length).toBe(1);
        const askLogContent = fs.readFileSync(path.join(runDir, askLogs[0]), 'utf8');
        expect(askLogContent).toContain('Does the report confirm the greeting is casual?');
        expect(askLogContent).toContain('Implemented the casual greeting.');
        expect(askLogContent).toContain('casual greeting was implemented');

        // ── UI: Run Log view shows the main flow with expandable sessions ────
        await page.goto(`/companies/ceo-co/run-logs/${rootRun.id}`);
        await expect(page.getByRole('heading', { name: `Run ${rootRun.name || '#' + rootRun.id} Details` })).toBeVisible({ timeout: 15_000 });
        await expect(page.getByTestId('run-current-status')).toHaveText(STATUS_LINE);

        const sessionBlocks = page.getByTestId('session-block');
        await expect(sessionBlocks).toHaveCount(1);
        await expect(sessionBlocks.first()).toContainText('CTO');
        await expect(sessionBlocks.first()).toContainText('Implement greeting feature');

        // Expand the CTO session and verify the nested log loads.
        await sessionBlocks.first().getByRole('button').first().click();
        await expect(sessionBlocks.first().getByText('Execution Log')).toBeVisible({ timeout: 15_000 });

        // Expanding the token bar reveals the per-agent breakdown across the
        // whole session tree (children and grandchildren).
        await page.locator('button[title="Click for detailed breakdown"]').first().click();
        const agentTokenStats = page.getByTestId('agent-token-stats');
        await expect(agentTokenStats).toBeVisible();
        await expect(agentTokenStats).toContainText('CEO');
        await expect(agentTokenStats).toContainText('CTO');
        await expect(agentTokenStats).toContainText('Coder');
        await expect(agentTokenStats).toContainText('QA');

        // Run Logs list: only the main session is a top-level card, and the
        // list is an overview only — no run's actual transcript is ever
        // fetched or rendered here, so the page stays fast regardless of log
        // history size. Full content lives on each run's own details page.
        await page.goto('/companies/ceo-co/runs');
        await expect(page.getByRole('heading', { name: 'Run Logs' })).toBeVisible();
        const rootCards = page.getByTestId('root-run-card');
        await expect(rootCards).toHaveCount(1);
        await expect(rootCards.first()).toContainText('1 session');

        // Expanding the root card reveals a stats/info panel (status, agent,
        // timing, token breakdown, nested sessions) — never the raw log.
        await expect(rootCards.first().getByText('Sessions (1)')).not.toBeVisible();
        await rootCards.first().locator('summary').click();
        await expect(rootCards.first().getByText('Sessions (1)')).toBeVisible();
        await expect(rootCards.first().getByText('Execution Log')).toHaveCount(0);

        const childLinks = rootCards.first().getByTestId('child-session-link');
        await expect(childLinks).toHaveCount(1);
        await expect(childLinks.first()).toContainText('CTO');

        // Expanding the token bar reveals the whole-tree per-agent breakdown,
        // computed client-side from the overview rows already on the page —
        // no extra network round trip.
        await rootCards.first().locator('button[title="Click for detailed breakdown"]').click();
        const listAgentStats = rootCards.first().getByTestId('agent-token-stats');
        await expect(listAgentStats).toBeVisible();
        await expect(listAgentStats).toContainText('CTO');

        // Following a nested session's link navigates to its own details
        // page, where the full transcript is fetched and rendered.
        await childLinks.first().click();
        await expect(page.getByRole('heading', { name: /Details$/ })).toBeVisible({ timeout: 15_000 });

        // Task view shows the untouched user input.
        await page.goto(`/companies/ceo-co/tasks/${taskId}`);
        await expect(page.locator('textarea')).toHaveValue('Add a greeting to the product.');

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

        const subtasksList = await (await request.get(`/api/tasks?company_id=${companyId}&parent_id=${taskId}`)).json();
        const subtask = (subtasksList as any[])[0];
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
        await expect(builtin.getByText('Chief Executive Officer — owns overall project execution and business decisions, works exclusively through delegation')).toBeHidden();
        await builtin.getByRole('button').first().click();
        await expect(builtin.getByText('Chief Executive Officer — owns overall project execution and business decisions, works exclusively through delegation')).toBeVisible();
        await expect(builtin.getByText('Chief Marketing Officer — owns marketing strategy and metrics, delegates execution to SMM, PPC Specialist and Post Writer')).toBeVisible();
        await expect(builtin.getByText('Coder — implements features from tech specs with high-quality, pattern-following code')).toBeVisible();
    });
});

async function postJSON(request: APIRequestContext, url: string, data: unknown): Promise<any> {
    const res = await request.post(url, { data });
    if (!res.ok()) {
        throw new Error(`POST ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}
