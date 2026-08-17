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

test.describe.serial('orchestrator messaging matrix', () => {
    test.beforeAll(async ({ request }) => resetE2E(request, env.E2E_MOCK_PROVIDER_URL));
    test.afterAll(async ({ request }) => resetE2E(request, env.E2E_MOCK_PROVIDER_URL));

    test('covers orchestrator-to-workers, workers-to-owner, sibling isolation, and CEO consultation', async ({ request }) => {
        const provider = await postJSON(request, '/api/providers', {
            name: 'messaging-matrix-mock', base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key', provider_type: 'openai', default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model,e2e-orchestrator-model,e2e-ceo-model,e2e-agent-a-model,e2e-agent-b-model',
        });
        const setting = await request.put('/api/default-model-settings/task_orchestrator', {
            data: { provider_id: provider.id, model: 'e2e-orchestrator-model' },
        });
        expect(setting.ok(), await setting.text()).toBeTruthy();

        const company = await postJSON(request, '/api/companies', {
            name: 'Messaging Matrix Co', short_name: 'msg-matrix', color: '#059669',
            description: 'A company used to verify every routed agent conversation.',
        });
        const ceo = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Matrix CEO', role_key: 'CEO', short_name: 'CEO',
            system_prompt: 'Make the product decision and report the decision.', model: 'e2e-ceo-model', provider_id: provider.id,
        });
        const agentA = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Matrix Agent A', role_key: 'backend', short_name: 'A',
            system_prompt: 'Implement the backend part and ask the owner when a decision is needed.', model: 'e2e-agent-a-model', provider_id: provider.id,
        });
        const agentB = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Matrix Agent B', role_key: 'qa', short_name: 'B',
            system_prompt: 'Verify the result independently and report evidence.', model: 'e2e-agent-b-model', provider_id: provider.id,
        });
        const sprint = await postJSON(request, '/api/sprints', {
            company_id: company.id, name: 'Messaging Matrix Sprint', goal: 'Verify routed communication across the task tree.',
        });
        const task = await postJSON(request, '/api/tasks', {
            company_id: company.id, sprint_id: sprint.id, agent_id: ceo.id, title: 'Exercise every messaging direction',
            description: 'Coordinate two workers, exchange routed answers, consult the CEO, and verify the final result.',
        });

        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, { model: 'e2e-orchestrator-model', entries: [
            { tool_call: { id: 'launch-a', name: 'run_new_session', arguments: { agent_name: agentA.name, prompt: 'Implement the primary change and ask the owner about any unresolved product decision.' } } },
            { tool_call: { id: 'ask-ceo', name: 'ask_ceo', arguments: { task_id: task.id, message: 'Should the result preserve the existing event ordering?' } } },
            { tool_call: { id: 'inspect-ceo', name: 'get_session', arguments: { session_id: 0 } } },
            { text: 'The workers and CEO consultation are now being monitored.' },
            { tool_call: { id: 'answer-a-owner', name: 'answer_message', arguments: { message_id: 0, answer: 'Preserve the existing event ordering and document the choice.' } } },
            { tool_call: { id: 'send-a', name: 'send_message_to_session', arguments: { session_id: 0, message: 'Confirm the implementation boundary and return your answer.' } } },
            { tool_call: { id: 'launch-b', name: 'run_new_session', arguments: { agent_name: agentB.name, prompt: 'Verify the primary change independently and report evidence.' } } },
            { text: 'Agent A answered; Agent B is now being started.' },
            { tool_call: { id: 'answer-b-owner', name: 'answer_message', arguments: { message_id: 0, answer: 'Verify independently and report the evidence.' } } },
            { tool_call: { id: 'send-b', name: 'send_message_to_session', arguments: { session_id: 0, message: 'Run an independent verification and return the evidence.' } } },
            { text: 'All routed questions have been answered.' },
            { tool_call: { id: 'finish-root', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'Messaging matrix completed.', result_details: 'Both workers and the CEO consultation completed through routed messages.' } } },
        ] });
        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, { model: 'e2e-agent-a-model', entries: [
            { tool_call: { id: 'a-ask-owner', name: 'ask_task_owner', arguments: { question: 'Should I preserve the existing event ordering?' } } },
            { tool_call: { id: 'a-answer', name: 'answer_message', arguments: { message_id: 0, answer: 'The implementation boundary is clear and safe.' } } },
            { tool_call: { id: 'a-finish', name: 'finish_work', arguments: { status: 'done', summary: 'Agent A completed the implementation.', details: 'Agent A answered the orchestrator and received the owner decision.' } } },
        ] });
        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, { model: 'e2e-agent-b-model', entries: [
            { tool_call: { id: 'b-ask-owner', name: 'ask_task_owner', arguments: { question: 'What evidence should I prioritize?' } } },
            { tool_call: { id: 'b-answer', name: 'answer_message', arguments: { message_id: 0, answer: 'Independent verification passed with the requested evidence.' } } },
            { tool_call: { id: 'b-finish', name: 'finish_work', arguments: { status: 'done', summary: 'Agent B completed verification.', details: 'Agent B answered the orchestrator independently.' } } },
        ] });
        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, { model: 'e2e-ceo-model', entries: [
            { tool_call: { id: 'ceo-answer', name: 'answer_message', arguments: { message_id: 0, answer: 'Preserve the existing event ordering.' } } },
            { tool_call: { id: 'ceo-finish', name: 'finish_task', arguments: { task_status: 'done', finish_status: 'CEO consultation completed.', result_details: 'The product decision is to preserve event ordering.' } } },
        ] });

        const kick = await request.put(`/api/tasks/${task.id}`, { data: { status: 'to-do' } });
        expect(kick.ok(), await kick.text()).toBeTruthy();
        await waitForTaskStatus(request, task.id, 'in-review', 120_000);
        await expect.poll(async () => {
            const response = await request.get(`/api/tasks/${task.id}/runs`);
            const runs = await response.json();
            return runs.find((run: any) => run.kind === 'task_orchestrator')?.status;
        }, { timeout: 30_000 }).toBe('completed');

        const runs = await (await request.get(`/api/tasks/${task.id}/runs`)).json();
        const orchestrator = runs.find((run: any) => run.kind === 'task_orchestrator');
        const consultation = runs.find((run: any) => run.kind === 'ceo_consultation');
        const workers = runs.filter((run: any) => run.kind === 'agent_session');
        expect(orchestrator).toBeTruthy();
        expect(consultation?.parent_run_id).toBe(orchestrator.id);
        expect(workers.map((run: any) => run.agent_id).sort()).toEqual([agentA.id, agentB.id].sort());
        expect(workers.every((run: any) => run.parent_run_id === orchestrator.id && run.status === 'completed')).toBeTruthy();
        expect(consultation.status).toBe('completed');

        const log = await (await request.get(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const bodies = (log.requests as any[]).filter((entry) => entry.path.includes('/chat/completions')).map((entry) => JSON.stringify(entry.body));
        const joined = bodies.join('\n');
        expect(joined).toContain('send_message_to_session');
        expect(joined).toContain('answer_message');
        expect(joined).toContain('ask_task_owner');
        expect(joined).toContain('ask_ceo');
        expect(joined).toContain('get_session');
        expect(joined).not.toContain('ask_agent');

        const workerRequests = (log.requests as any[]).filter((entry) => ['e2e-agent-a-model', 'e2e-agent-b-model'].includes(entry.body?.model));
        expect(workerRequests.some((entry) => JSON.stringify(entry.body?.tools).includes('answer_message'))).toBeTruthy();
        expect(workerRequests.every((entry) => !JSON.stringify(entry.body?.tools).includes('ask_human'))).toBeTruthy();
        const ceoRequests = (log.requests as any[]).filter((entry) => entry.body?.model === 'e2e-ceo-model');
        expect(ceoRequests.some((entry) => JSON.stringify(entry.body?.tools).includes('answer_message'))).toBeTruthy();
        expect(ceoRequests.some((entry) => JSON.stringify(entry.body?.tools).includes('ask_human'))).toBeTruthy();
    });
});
