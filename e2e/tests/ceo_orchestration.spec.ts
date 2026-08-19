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
        await waitForTaskStatus(request, task.id, 'in-review', 90_000);

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
