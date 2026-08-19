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

test.describe.serial('task sidecar orchestrator', () => {
    let companyId = 0;
    let taskId = 0;

    test.beforeAll(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test.afterAll(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test('CEO-owned task is executed by an orchestrator that starts, answers, and monitors a worker', async ({ request }) => {
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
            description: 'A company shipping privacy-first clinical analytics.',
        });
        companyId = company.id;
        const ceo = await postJSON(request, '/api/agents', {
            company_id: companyId, name: 'CEO', role_key: 'CEO', short_name: 'CEO',
            description: 'Product owner who defines business outcomes and acceptance.',
            system_prompt: 'You are the CEO product owner.', model: 'e2e-mock-model', provider_id: provider.id,
        });
        const worker = await postJSON(request, '/api/agents', {
            company_id: companyId, name: 'Backend Builder', role_key: 'backend', short_name: 'BE',
            description: 'Implements APIs and database changes.',
            system_prompt: 'You are the implementation worker.', model: 'e2e-mock-model', provider_id: provider.id,
        });
        const sprint = await postJSON(request, '/api/sprints', {
            company_id: companyId, name: 'Orchestrator Sprint', goal: 'Ship the audit export beta',
        });
        const project = await postJSON(request, '/api/projects', {
            company_id: companyId, name: 'Care Portal', description: 'Patient-facing audit reporting portal',
        });

        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            method: 'POST', headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ model: 'e2e-mock-model', entries: [
                { tool_call: { id: 'worker-status', name: 'report_status', arguments: { status: 'implementing the audit export' } } },
                { tool_call: { id: 'worker-finish', name: 'finish_task', arguments: { task_status: 'done', finish_status: 'worker complete', result_details: 'worker complete' } } },
            ] }),
        });
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            method: 'POST', headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ model: 'e2e-orchestrator-model', entries: [
                { tool_call: { id: 'orch-list', name: 'get_session_list', arguments: {} } },
                { tool_call: { id: 'orch-run', name: 'run_new_session', arguments: {
                    agent_name: 'Backend Builder', title: 'Implement audit export', prompt: 'Implement the audit CSV export, preserve event ordering, and run the focused tests.',
                } } },
                { text: 'Worker session started with the implementation brief.' },
                { tool_call: { id: 'orch-status-1', name: 'get_session', arguments: { session_id: 2 } } },
                { tool_call: { id: 'orch-status-2', name: 'get_session', arguments: { session_id: 2 } } },
                { text: 'Worker execution is complete.' },
            ] }),
        });
        const task = await postJSON(request, '/api/tasks', {
            company_id: companyId, project_id: project.id, sprint_id: sprint.id, agent_id: ceo.id,
            title: 'Add audit export', description: 'Export a patient audit trail as CSV.',
            refined_description: 'Use the existing event ordering.',
            acceptance_criteria: 'CSV downloads with stable headers', test_cases: 'Empty audit trail; large audit trail',
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
            return runs.some((r: any) => r.name?.endsWith('-orchestrator'));
        }, { timeout: 20_000 }).toBeTruthy();

        const runsResponse = await request.get(`/api/tasks/${taskId}/runs`);
        const runs = await runsResponse.json();
        const orchestrator = runs.find((r: any) => r.name?.endsWith('-orchestrator'));
        expect(orchestrator).toBeTruthy();
        expect(orchestrator.parent_run_id).toBeNull();
        const workers = runs.filter((r: any) => r.parent_run_id === orchestrator.id);
        expect(workers.length).toBe(1);
        expect(workers[0].agent_id).toBe(worker.id);
        expect(workers[0].root_run_id).toBe(orchestrator.id);
        const taskResponse = await request.get(`/api/tasks/${taskId}`);
        expect(taskResponse.ok()).toBeTruthy();
        expect((await taskResponse.json()).orchestrator_run_id).toBe(orchestrator.id);

        const mockLog = await (await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const orchestratorRequests = (mockLog.requests as any[]).filter(r => r.body?.model === 'e2e-orchestrator-model');
        expect(orchestratorRequests.length).toBeGreaterThanOrEqual(1);
        const toolNameSets = orchestratorRequests.map((r: any) =>
            ((r.body?.tools || []) as any[]).map((t: any) => t.function?.name).sort());
        expect(toolNameSets.some((names: string[]) => names.includes('send_message_to_session'))).toBeTruthy();
        expect(toolNameSets.some((names: string[]) => names.includes('run_new_session'))).toBeTruthy();
        expect(JSON.stringify(toolNameSets)).not.toContain('ask_agent');

        const statusRequests = orchestratorRequests.filter((r: any) =>
            JSON.stringify(r.body?.messages || []).includes('last_reported_status'));
        expect(statusRequests.length).toBeGreaterThanOrEqual(1);
        const allOrchestratorJSON = JSON.stringify(orchestratorRequests);
        expect(allOrchestratorJSON).toContain('A company shipping privacy-first clinical analytics.');
        expect(allOrchestratorJSON).toContain('Patient-facing audit reporting portal');
        expect(allOrchestratorJSON).toContain('Ship the audit export beta');
        expect(allOrchestratorJSON).toContain('Export a patient audit trail as CSV.');
        expect(allOrchestratorJSON).toContain('Backend Builder');
        expect(allOrchestratorJSON).toContain('Implements APIs and database changes.');
        expect(allOrchestratorJSON).toContain('agent_name');
        expect(allOrchestratorJSON).toContain('child_statuses');
        expect(allOrchestratorJSON).toContain('up to five');
        expect(JSON.stringify(statusRequests)).toContain('implementing the audit export');
        expect(JSON.stringify(statusRequests)).toContain('last_reported_at');
        expect(JSON.stringify(statusRequests)).toContain('run_status_history');
        const reportEventRequests = orchestratorRequests.filter((r: any) =>
            (r.body?.messages || []).some((m: any) =>
                typeof m.content === 'string' && m.content.includes('"event_type":"status_report"')));
        expect(reportEventRequests.length).toBeGreaterThanOrEqual(1);
        const statusPayloads = statusRequests.flatMap((r: any) => (r.body?.messages || [])
            .filter((m: any) => m.role === 'tool' && typeof m.content === 'string')
            .map((m: any) => {
                try { return JSON.parse(m.content); } catch { return null; }
            })
            .filter((p: any) => p?.last_run_status?.last_reported_status?.includes('implementing the audit export')));
        expect(statusPayloads.length).toBeGreaterThanOrEqual(1);
        expect(statusPayloads[0].last_run_status.last_reported_message_id).toBeGreaterThan(0);

    });
});
