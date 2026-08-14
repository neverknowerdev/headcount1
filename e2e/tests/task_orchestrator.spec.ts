import { test, expect } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';

const env = loadE2EEnv();

async function postJSON(request: any, url: string, data: unknown): Promise<any> {
    const response = await request.post(url, { data });
    expect(response.ok(), `${url}: ${await response.text()}`).toBeTruthy();
    return response.json();
}

test.describe.serial('task sidecar orchestrator', () => {
    let companyId = 0;
    let taskId = 0;

    test.beforeAll(async ({ request }) => {
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
    });

    test.afterAll(async ({ request }) => {
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
    });

    test('uses a separate model and only manages worker sessions', async ({ request }) => {
        const provider = await postJSON(request, '/api/providers', {
            name: 'orchestrator-e2e', base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key', provider_type: 'openai',
            default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model,e2e-orchestrator-model',
        });
        const setting = await request.put('/api/default-model-settings/task_orchestrator', {
            data: { provider_id: provider.id, model: 'e2e-orchestrator-model' },
        });
        expect(setting.ok(), await setting.text()).toBeTruthy();

        const company = await postJSON(request, '/api/companies', {
            name: 'Orchestrator E2E', short_name: 'orch-e2e', color: '#4f46e5',
        });
        companyId = company.id;
        const agent = await postJSON(request, '/api/agents', {
            company_id: companyId, name: 'Worker', role_key: 'Worker', short_name: 'WRK',
            system_prompt: 'You are the worker.', model: 'e2e-mock-model', provider_id: provider.id,
        });
        const sprint = await postJSON(request, '/api/sprints', { company_id: companyId, name: 'Orchestrator Sprint' });

        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            method: 'POST', headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ model: 'e2e-mock-model', entries: [{ tool_call: { id: 'worker-finish', name: 'finish_task', arguments: { task_status: 'done', finish_status: 'worker complete', result_details: 'worker complete' } } }] }),
        });
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            method: 'POST', headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ model: 'e2e-orchestrator-model', entries: [
                { tool_call: { id: 'orch-list', name: 'get_sessions', arguments: {} } },
                { text: 'Observed worker; no intervention required.' },
                { text: 'Execution is terminal.' },
            ] }),
        });
        const task = await postJSON(request, '/api/tasks', {
            company_id: companyId, sprint_id: sprint.id, agent_id: agent.id,
            title: 'Orchestrator smoke task', description: 'Complete the smoke task.', task_type: 'implement',
        });
        taskId = task.id;

        // Creating a task leaves it in the backlog. Assigning it to To Do is
        // the explicit execution trigger used by the task lifecycle API.
        const kick = await request.put(`/api/tasks/${taskId}`, {
            data: { status: 'to-do' },
        });
        expect(kick.ok(), await kick.text()).toBeTruthy();

        await waitForTaskStatus(request, taskId, 'done');
        await expect.poll(async () => {
            const response = await request.get(`/api/tasks/${taskId}/runs`);
            const runs = await response.json();
            return runs.some((r: any) => r.supervised_run_id);
        }, { timeout: 20_000 }).toBeTruthy();

        const runsResponse = await request.get(`/api/tasks/${taskId}/runs`);
        const runs = await runsResponse.json();
        const workers = runs.filter((r: any) => !r.supervised_run_id);
        const orchestrators = runs.filter((r: any) => r.supervised_run_id);
        expect(workers.length).toBe(1);
        expect(orchestrators.length).toBe(1);
        expect(orchestrators[0].supervised_run_id).toBe(workers[0].id);

        const mockLog = await (await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const orchestratorRequests = (mockLog.requests as any[]).filter(r => r.body?.model === 'e2e-orchestrator-model');
        expect(orchestratorRequests.length).toBeGreaterThanOrEqual(1);
        const expectedTools = [
            'ask_task_owner', 'fork_session', 'get_session_status', 'get_sessions', 'run_new_session', 'stop_session',
        ];
        const toolNameSets = orchestratorRequests.map((r: any) =>
            ((r.body?.tools || []) as any[]).map((t: any) => t.function?.name).sort());
        expect(toolNameSets).toContainEqual(expectedTools);
    });
});
