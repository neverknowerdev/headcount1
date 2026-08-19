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

test.describe.serial('orchestrator reserved Worker sessions', () => {
    test.beforeAll(async ({ request }) => resetE2E(request, env.E2E_MOCK_PROVIDER_URL));
    test.afterAll(async ({ request }) => resetE2E(request, env.E2E_MOCK_PROVIDER_URL));

    test('runs a bounded Worker job with helper tools and then delegates task work', async ({ request }) => {
        const provider = await postJSON(request, '/api/providers', {
            name: 'reserved-worker-mock', base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key', provider_type: 'openai', default_model: 'e2e-agent-model',
            supported_models: 'e2e-agent-model,e2e-helper-model,e2e-orchestrator-model',
        });
        for (const [purpose, model] of [['task_orchestrator', 'e2e-orchestrator-model'], ['helper_worker', 'e2e-helper-model']]) {
            const setting = await request.put(`/api/default-model-settings/${purpose}`, { data: { provider_id: provider.id, model } });
            expect(setting.ok(), await setting.text()).toBeTruthy();
        }
        const company = await postJSON(request, '/api/companies', { name: 'Reserved Worker Co', short_name: 'reserved-worker', color: '#0f766e' });
        const ceo = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Worker Test CEO', role_key: 'CEO', short_name: 'CEO',
            system_prompt: 'Own the task outcome.', model: 'e2e-agent-model', provider_id: provider.id,
        });
        const coder = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Worker Test Coder', role_key: 'coder', short_name: 'CODER',
            system_prompt: 'Implement the task and finish it for review.', model: 'e2e-agent-model', provider_id: provider.id,
        });
        const sprint = await postJSON(request, '/api/sprints', { company_id: company.id, name: 'Worker Sprint', goal: 'Verify reserved worker routing.' });
        const task = await postJSON(request, '/api/tasks', {
            company_id: company.id, sprint_id: sprint.id, agent_id: ceo.id,
            title: 'Verify reserved worker routing', description: 'Run one bounded auxiliary job before implementation.',
        });

        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            model: 'e2e-helper-model', entries: [
                { tool_call: { id: 'reserved-worker-finish', name: 'finish_work', arguments: { status: 'done', summary: 'Repository verification complete.', details: 'The bounded helper job returned its evidence.' } } },
            ],
        });
        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            model: 'e2e-agent-model', entries: [
                { tool_call: { id: 'coder-finish', name: 'finish_task', arguments: { task_status: 'done', finish_status: 'Implementation complete.', result_details: 'The delegated task work completed.' } } },
            ],
        });
        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            model: 'e2e-orchestrator-model', entries: [
                { tool_call: { id: 'launch-reserved-worker', name: 'run_new_session', arguments: { agent_name: 'Worker', title: 'Verify repository state', prompt: 'Run one bounded repository verification and return evidence without changing implementation.' } } },
                { tool_call: { id: 'launch-coder', name: 'run_new_session', arguments: { agent_name: coder.name, title: 'Implement task', prompt: 'Implement the task after the auxiliary verification and finish it for review.' } } },
                { text: 'The auxiliary verification and implementation delegation are complete.' },
            ],
        });

        const kick = await request.put(`/api/tasks/${task.id}`, { data: { status: 'to-do' } });
        expect(kick.ok(), await kick.text()).toBeTruthy();
        await waitForTaskStatus(request, task.id, 'done', 90_000);

        const runs = await (await request.get(`/api/tasks/${task.id}/runs`)).json();
        const orchestrator = runs.find((run: any) => run.kind === 'task_orchestrator');
        const reservedWorker = runs.find((run: any) => run.kind === 'helper_worker' && run.title === 'Verify repository state');
        const coderRun = runs.find((run: any) => run.kind === 'agent_session' && run.title === 'Implement task');
        expect(orchestrator).toBeTruthy();
        expect(reservedWorker, JSON.stringify(runs)).toBeTruthy();
        expect(reservedWorker.agent_name).toBe('Worker');
        expect(reservedWorker.status).toBe('completed');
        expect(reservedWorker.workspace_path).toBeTruthy();
        expect(coderRun, JSON.stringify(runs)).toBeTruthy();
        expect(coderRun.status).toBe('completed');

        const log = await (await request.get(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const requests = (log.requests as any[]).filter((entry) => String(entry.path).includes('/chat/completions'));
        expect(requests.some((entry) => entry.body?.model === 'e2e-helper-model')).toBeTruthy();
        expect(requests.some((entry) => entry.body?.model === 'e2e-agent-model')).toBeTruthy();
        expect(JSON.stringify(requests)).toContain('finish_work');
        expect(JSON.stringify(requests)).toContain('Verify repository state');
    });
});
