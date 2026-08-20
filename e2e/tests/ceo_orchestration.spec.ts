import { test, expect } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';
import { resetE2E } from '../helpers/reset';

const env = loadE2EEnv();

async function postJSON(request: any, url: string, data: unknown): Promise<any> {
    const response = await request.post(url, { data });
    expect(response.ok(), `${url}: ${await response.text()}`).toBeTruthy();
    return response.json();
}

test.describe.serial('CEO consultation and durable orchestration', () => {
    test.beforeAll(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test.afterAll(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test('orchestrator consults the CEO, launches a session, and preserves the run tree', async ({ request }) => {
        const provider = await postJSON(request, '/api/providers', {
            name: 'ceo-consultation-mock', base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key', provider_type: 'openai',
            default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model,e2e-orchestrator-model,e2e-ceo-model',
        });
        const setting = await request.put('/api/default-model-settings/task_orchestrator', {
            data: { provider_id: provider.id, model: 'e2e-orchestrator-model' },
        });
        expect(setting.ok(), await setting.text()).toBeTruthy();

        const company = await postJSON(request, '/api/companies', {
            name: 'Consultation Co', short_name: 'consult-co', color: '#4f46e5',
            description: 'A company shipping durable workflow tooling.',
        });
        const ceo = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Chief Executive Officer', role_key: 'CEO', short_name: 'CEO',
            system_prompt: 'You own the product decision.', model: 'e2e-ceo-model', provider_id: provider.id,
        });
        const worker = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Implementation Agent', role_key: 'backend', short_name: 'BE',
            system_prompt: 'Implement the approved work.', model: 'e2e-mock-model', provider_id: provider.id,
        });
        const sprint = await postJSON(request, '/api/sprints', {
            company_id: company.id, name: 'Consultation Sprint', goal: 'Ship the audit export',
        });
        const task = await postJSON(request, '/api/tasks', {
            company_id: company.id, sprint_id: sprint.id, agent_id: ceo.id,
            title: 'Add audit export', description: 'Export a patient audit trail as CSV.',
        });

        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            method: 'POST', headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ model: 'e2e-orchestrator-model', entries: [
                { tool_call: { id: 'consult', name: 'ask_ceo', arguments: {
                    task_id: task.id, message: 'Should the export preserve the existing event ordering?',
                } } },
                { tool_call: { id: 'launch', name: 'run_new_session', arguments: {
                    agent_name: 'Implementation Agent', title: 'Implement audit export', prompt: 'Implement the audit export using the CEO decision.',
                } } },
                { text: 'The implementation session is complete.' },
                { tool_call: { id: 'orchestrator-finish', name: 'finish_task', arguments: { summary: 'The CEO decision was applied and the implementation result was verified.' } } },
            ] }),
        });
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            method: 'POST', headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ model: 'e2e-mock-model', entries: [
                { tool_call: { id: 'finish', name: 'finish_task', arguments: {
                    task_status: 'in-review', finish_status: 'Audit export is ready for review.',
                    result_details: 'Implemented with stable event ordering.',
                } } },
            ] }),
        });

        const kick = await request.put(`/api/tasks/${task.id}`, { data: { status: 'to-do' } });
        expect(kick.ok(), await kick.text()).toBeTruthy();
        await waitForTaskStatus(request, task.id, 'done', 90_000);

        const runs = await (await request.get(`/api/tasks/${task.id}/runs`)).json();
        const orchestrator = runs.find((run: any) => run.kind === 'task_orchestrator');
        const consultation = runs.find((run: any) => run.kind === 'ceo_consultation');
        const session = runs.find((run: any) => run.kind === 'agent_session');
        expect(orchestrator).toBeTruthy();
        expect(consultation).toBeTruthy();
        expect(session).toBeTruthy();
        await expect.poll(async () => {
            const response = await request.get(`/api/tasks/${task.id}/runs`);
            const currentRuns = await response.json();
            return currentRuns.find((run: any) => run.kind === 'task_orchestrator')?.status;
        }, { timeout: 20_000 }).toBe('completed');
        const settledRuns = await (await request.get(`/api/tasks/${task.id}/runs`)).json();
        const settledOrchestrator = settledRuns.find((run: any) => run.kind === 'task_orchestrator');
        const settledConsultation = settledRuns.find((run: any) => run.kind === 'ceo_consultation');
        const settledSession = settledRuns.find((run: any) => run.kind === 'agent_session');
        expect(settledConsultation.parent_run_id).toBe(settledOrchestrator.id);
        expect(settledSession.parent_run_id).toBe(settledOrchestrator.id);
        expect(settledSession.agent_id).toBe(worker.id);
        expect(settledOrchestrator.status).toBe('completed');
        expect(settledConsultation.status).toBe('completed');
        expect(settledSession.status).toBe('completed');

        // Direct children of the root: exactly one CTO session.
        const rootChildren = await (await request.get(`/api/runs/${rootRun.id}/children`)).json();
        expect(rootChildren.length).toBe(1);
        const ctoRun = rootChildren[0];
        expect(ctoRun.agent_id).toBe(ctoAgent.id);
        expect(ctoRun.parent_run_id).toBe(rootRun.id);
        expect(ctoRun.root_run_id).toBe(rootRun.id);
        expect(ctoRun.status).toBe('completed');

        // Direct children of the CTO session: Coder then QA.
        const ctoChildren = await (await request.get(`/api/runs/${ctoRun.id}/children`)).json();
        expect((ctoChildren as any[]).map(c => c.agent_id)).toEqual([coderAgent.id, qaAgent.id]);
        for (const child of ctoChildren) {
            expect(child.parent_run_id).toBe(ctoRun.id);
            expect(child.root_run_id).toBe(rootRun.id);
            expect(child.status).toBe('completed');
        }

        // ?deep=true returns the whole tree from the root.
        const deepChildren = await (await request.get(`/api/runs/${rootRun.id}/children?deep=true`)).json();
        expect((deepChildren as any[]).map(c => c.agent_id).sort((a, b) => a - b)).toEqual([ctoAgent.id, coderAgent.id, qaAgent.id].sort((a, b) => a - b));

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
        expect(ctoTask.agent_id).toBe(ctoAgent.id);
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
        expect(coderTask.agent_id).toBe(coderAgent.id);
        expect(coderTask.refined_description).toBe('Add a casual greeting to the home page.');
        const qaTask = (ctoSubtasks as any[]).find(t => t.title === 'Verify greeting');
        expect(qaTask.agent_id).toBe(qaAgent.id);

        // ── Filesystem: JSONL logs grouped by main run id, one file per session ─
        const basePath = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');
        const runDir = path.join(basePath, 'logs', 'ceo-co', String(taskId), `run-${rootRun.id}`);
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

        // The ask_artifact reader exchange is recorded in the normal JSONL
        // stream alongside the rest of the session's LLM traffic.
        const mainLogContent = JSON.stringify(mainEntries);
        expect(mainLogContent).toContain('Does the report confirm the greeting is casual?');
        expect(mainLogContent).toContain('Implemented the casual greeting.');
        expect(mainLogContent).toContain('casual greeting was implemented');

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
        await requireFetchOK(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' }, 5_000);

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

    test('agents page lists all built-in agents with enable controls', async ({ page }) => {
        await page.goto('/companies/ceo-co/agents');
        const builtin = page.getByTestId('builtin-agents');
        await expect(builtin).toBeVisible();
        await expect(builtin).toContainText('Built-in agents');
        await expect(builtin.getByText('CEO')).toBeVisible();
        await expect(builtin.getByText('CMO')).toBeVisible();
        await expect(builtin.getByText('QA Manual')).toBeVisible();
        await expect(builtin.getByRole('button', { name: 'Disable agent' }).first()).toBeVisible();
        const log = await (await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const orchestrationRequests = (log.requests as any[]).filter((entry) => entry.body?.model === 'e2e-orchestrator-model');
        expect(JSON.stringify(orchestrationRequests)).toContain('ask_ceo');
        expect(JSON.stringify(orchestrationRequests)).toContain('run_new_session');
        expect(JSON.stringify(orchestrationRequests)).not.toContain('ask_agent');
        const consultationRequests = (log.requests as any[]).filter((entry) => entry.body?.model === 'e2e-ceo-model');
        expect(consultationRequests.some((entry) => JSON.stringify(entry.body?.tools).includes('answer_message'))).toBeTruthy();
        expect(JSON.stringify(consultationRequests)).toContain('Use the existing event ordering');
    });
});
