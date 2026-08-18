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

test.describe.serial('full orchestrator lifecycle and recovery', () => {
    test.beforeAll(async ({ request }) => resetE2E(request, env.E2E_MOCK_PROVIDER_URL));
    test.afterAll(async ({ request }) => resetE2E(request, env.E2E_MOCK_PROVIDER_URL));

    test('coordinates CTO research, architecture routing, QA repair, stop, fork, and re-verification', async ({ request }) => {
        const provider = await postJSON(request, '/api/providers', {
            name: 'full-lifecycle-mock', base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key', provider_type: 'openai', default_model: 'e2e-helper-model',
            supported_models: [
                'e2e-helper-model', 'e2e-orchestrator-model', 'e2e-cto-model',
                'e2e-coder-model', 'e2e-qa-model',
            ].join(','),
        });
        for (const [purpose, model] of [['task_orchestrator', 'e2e-orchestrator-model'], ['helper_worker', 'e2e-helper-model']]) {
            const setting = await request.put(`/api/default-model-settings/${purpose}`, {
                data: { provider_id: provider.id, model },
            });
            expect(setting.ok(), await setting.text()).toBeTruthy();
        }

        const company = await postJSON(request, '/api/companies', {
            name: 'Full Lifecycle Co', short_name: 'full-life', color: '#7c3aed',
            description: 'A deterministic end-to-end orchestration lifecycle.',
        });
        const ceo = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Lifecycle CEO', role_key: 'CEO', short_name: 'CEO',
            system_prompt: 'Own the final product decision.', model: 'e2e-cto-model', provider_id: provider.id,
        });
        const cto = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Lifecycle CTO', role_key: 'CTO', short_name: 'CTO',
            system_prompt: 'Design the technical solution, use helper workers, and answer architecture questions.',
            model: 'e2e-cto-model', provider_id: provider.id, can_use_workers: true,
        });
        const coder = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Lifecycle Coder', role_key: 'backend', short_name: 'CODER',
            system_prompt: 'Implement the approved technical design and report blockers to the task owner.',
            model: 'e2e-coder-model', provider_id: provider.id,
        });
        const qa = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Lifecycle QA', role_key: 'qa', short_name: 'QA',
            system_prompt: 'Test the implementation independently and report every issue.',
            model: 'e2e-qa-model', provider_id: provider.id,
        });
        const sprint = await postJSON(request, '/api/sprints', {
            company_id: company.id, name: 'Full Lifecycle Sprint', goal: 'Deliver and verify the routed implementation.',
        });
        const task = await postJSON(request, '/api/tasks', {
            company_id: company.id, sprint_id: sprint.id, agent_id: ceo.id,
            title: 'Build the routed implementation',
            description: 'Design, implement, test, repair, fork, and re-test one large task run.',
        });

        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            model: 'e2e-helper-model', entries: [
                { tool_call: { id: 'helper-finish', name: 'finish_work', arguments: {
                    status: 'done',
                    summary: 'Repository inspection completed.',
                    details: 'The helper returned its bounded one-shot evidence to the CTO.',
                } } },
            ],
        });

        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            model: 'e2e-orchestrator-model', entries: [
                { tool_call: { id: 'launch-cto', name: 'run_new_session', arguments: { agent_name: cto.name, prompt: 'Design the technical solution, coordinate research workers, write the technical specification artifact, and finish the design handoff.' } } },
                { tool_call: { id: 'inspect-cto-1', name: 'get_session', arguments: { session_id: 0 } } },
                { tool_call: { id: 'inspect-cto-2', name: 'get_session', arguments: { session_id: 0 } } },
                { text: 'The CTO design stream is active and its worker evidence is being monitored.' },
                { tool_call: { id: 'inspect-cto-complete', name: 'get_session', arguments: { session_id: 0 } } },
                { tool_call: { id: 'launch-coder', name: 'run_new_session', arguments: { agent_name: coder.name, prompt: 'Implement the CTO technical specification and ask the task owner about architecture if needed.' } } },
                { tool_call: { id: 'inspect-coder', name: 'get_session', arguments: { session_id: 0 } } },
                { tool_call: { id: 'route-coder-to-cto', name: 'send_message_to_session', arguments: { session_id: 0, message: 'Answer the Coder architecture question using the completed technical specification.' } } },
                { tool_call: { id: 'answer-coder', name: 'answer_message', arguments: { message_id: 0, answer: 'Use the event-driven repository boundary and keep the controller as the source of truth.' } } },
                { tool_call: { id: 'inspect-cto-after-question', name: 'get_session', arguments: { session_id: 0 } } },
                { text: 'The Coder received the CTO architecture decision.' },
                { tool_call: { id: 'launch-qa', name: 'run_new_session', arguments: { agent_name: qa.name, prompt: 'Test the implementation independently and report every issue to the task owner.' } } },
                { tool_call: { id: 'inspect-qa', name: 'get_session', arguments: { session_id: 0 } } },
                { tool_call: { id: 'route-qa-issue-to-coder', name: 'send_message_to_session', arguments: { session_id: 0, message: 'Fix the QA issue, preserve the architecture decision, and report the repair.' } } },
                { tool_call: { id: 'answer-qa', name: 'answer_message', arguments: { message_id: 0, answer: 'The Coder has received the issue and is applying the repair.' } } },
                { tool_call: { id: 'inspect-coder-before-stop', name: 'get_session', arguments: { session_id: 0 } } },
                { tool_call: { id: 'stop-coder', name: 'stop_session', arguments: { session_id: 0, reason: 'The Coder reported a bad/stale status and needs controlled recovery.' } } },
                { tool_call: { id: 'fork-coder', name: 'fork_session', arguments: { session_id: 0, fork_message_id: 0 } } },
                { tool_call: { id: 'inspect-forked-coder', name: 'get_session', arguments: { session_id: 0 } } },
                { tool_call: { id: 'inspect-forked-coder-again', name: 'get_session', arguments: { session_id: 0 } } },
                { tool_call: { id: 'launch-qa-retry', name: 'run_new_session', arguments: { agent_name: qa.name, prompt: 'Re-verify the repaired implementation from the fork; all regression checks must pass.' } } },
                { tool_call: { id: 'inspect-qa-retry', name: 'get_session', arguments: { session_id: 0 } } },
                { text: 'Final verification is complete; the repaired implementation is ready.' },
                { tool_call: { id: 'orchestrator-finish', name: 'finish_task', arguments: { task_status: 'done', finish_status: 'Task execution completed after final verification.', result_details: 'The CTO design, routed architecture answer, Coder repair, fork replay, and final QA verification all completed successfully.' } } },
            ],
        });

        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            model: 'e2e-cto-model',
            entries: [
                { tool_call: { id: 'cto-status-design', name: 'report_status', arguments: { status: 'Designing technical boundaries and acceptance risks.' } } },
                { tool_calls: [
                    { id: 'cto-explore-a', name: 'run_worker', arguments: { prompt: 'Explore the repository structure and report the important integration points.' } },
                    { id: 'cto-explore-b', name: 'run_worker', arguments: { prompt: 'Explore existing tests and report reusable fixtures and race risks.' } },
                ] },
                { tool_call: { id: 'cto-status-exploration', name: 'report_status', arguments: { status: 'Reviewing the first two repository overviews.' } } },
                { tool_call: { id: 'cto-workers-1', name: 'worker_list', arguments: {} } },
                { tool_call: { id: 'cto-workers-2', name: 'worker_list', arguments: {} } },
                { tool_calls: [
                    { id: 'cto-followup-a', name: 'run_worker', arguments: { prompt: 'Follow up on the repository integration question and identify the safest implementation boundary.' } },
                    { id: 'cto-followup-b', name: 'run_worker', arguments: { prompt: 'Follow up on the race-risk question and propose focused verification cases.' } },
                ] },
                { tool_call: { id: 'cto-status-followup', name: 'report_status', arguments: { status: 'Synthesizing follow-up answers into the technical specification.' } } },
                { tool_call: { id: 'cto-workers-3', name: 'worker_list', arguments: {} } },
                { tool_call: { id: 'cto-workers-4', name: 'worker_list', arguments: {} } },
                { tool_call: { id: 'cto-spec', name: 'write_artifact', arguments: { filename: 'technical-spec.md', content: '# Routed implementation specification\n\nThe controller owns durable state and routed answers.', description: 'CTO technical specification.' } } },
                { tool_call: { id: 'cto-status-ready', name: 'report_status', arguments: { status: 'Technical specification written and ready for implementation.' } } },
                { tool_call: { id: 'cto-finish', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'Technical design completed.', result_details: 'The technical specification is stored in technical-spec.md; repository exploration and follow-up worker evidence were incorporated.' } } },
            ],
            inbound_entries: [
                { tool_call: { id: 'cto-answer-architecture', name: 'answer_message', arguments: { message_id: 0, answer: 'Use the event-driven repository boundary and keep the controller as the source of truth.' } } },
                { tool_call: { id: 'cto-replacement-finish', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'Architecture clarification delivered.', result_details: 'The completed CTO design was rehydrated to answer the Coder.' } } },
            ],
        });

        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            model: 'e2e-coder-model', entries: [
                { tool_call: { id: 'coder-status-1', name: 'report_status', arguments: { status: 'Implementing the approved technical specification.' } } },
                { tool_call: { id: 'coder-write-1', name: 'write', arguments: { path: 'controller-state.txt', content: 'controller owns durable state' } } },
                { tool_call: { id: 'coder-status-2', name: 'report_status', arguments: { status: 'Implementation is progressing; checking the architecture boundary.' } } },
                { tool_call: { id: 'coder-ask-cto', name: 'ask_task_owner', arguments: { question: 'Should the controller or the worker own the routed event transition?' } } },
                { tool_call: { id: 'coder-write-2', name: 'write', arguments: { path: 'architecture-decision.txt', content: 'controller owns the routed event transition' } } },
                { tool_call: { id: 'coder-finish', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'Implementation completed.', result_details: 'Implemented the controller boundary and applied the CTO architecture answer.' } } },
            ],
            inbound_entries: [
                { tool_call: { id: 'coder-answer-qa', name: 'answer_message', arguments: { message_id: 0, answer: 'The QA issue is understood; I am applying the requested repair.' } } },
                { tool_call: { id: 'coder-write-fix', name: 'write', arguments: { path: 'qa-fix.txt', content: 'repair applied' } } },
                { tool_call: { id: 'coder-status-bad', name: 'report_status', arguments: { status: 'Bad status: repair is blocked by an unsafe partial workspace state.' } } },
                { tool_call: { id: 'coder-status-stale', name: 'report_status', arguments: { status: 'Stale status: no safe progress can be claimed.' } } },
                // Keep this replacement session alive after its bad status so
                // the orchestrator exercises an explicit stop before forking,
                // rather than recovering a session that already failed.
                { tool_call: { id: 'coder-wait-for-recovery', name: 'answer_message', arguments: { message_id: 0, answer: 'Waiting for the orchestrator to choose a safe recovery boundary.' } } },
            ],
            fork_entries: [
                { tool_call: { id: 'coder-fork-finish', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'Forked repair completed.', result_details: 'The fork replayed the prior write and completed the repair from the safe boundary.' } } },
            ],
        });

        await postJSON(request, `${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            model: 'e2e-qa-model', entries: [
                { tool_call: { id: 'qa-status', name: 'report_status', arguments: { status: 'Running independent verification.' } } },
                { tool_call: { id: 'qa-issue', name: 'ask_task_owner', arguments: { question: 'Found a regression: the repair must preserve the controller-owned event transition.' } } },
                { tool_call: { id: 'qa-finish', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'QA found an issue and is awaiting the repair handoff.', result_details: 'The first verification identified a concrete repair request.' } } },
            ],
            retry_entries: [
                { tool_call: { id: 'qa-retry-status', name: 'report_status', arguments: { status: 'Re-running the full regression suite after the forked repair.' } } },
                { tool_call: { id: 'qa-retry-finish', name: 'finish_task', arguments: { task_status: 'done', finish_status: 'QA passed all final verification checks.', result_details: 'The forked repair preserves the architecture decision and all regression checks pass.' } } },
            ],
        });

        const kick = await request.put(`/api/tasks/${task.id}`, { data: { status: 'to-do' } });
        expect(kick.ok(), await kick.text()).toBeTruthy();
        await waitForTaskStatus(request, task.id, 'done', 180_000);

        await expect.poll(async () => {
            const response = await request.get(`/api/tasks/${task.id}/runs`);
            const runs = await response.json();
            return runs.find((run: any) => run.kind === 'task_orchestrator')?.status;
        }, { timeout: 45_000 }).toBe('completed');

        const runs = await (await request.get(`/api/tasks/${task.id}/runs`)).json();
        const orchestrator = runs.find((run: any) => run.kind === 'task_orchestrator');
        const agentSessions = runs.filter((run: any) => run.kind === 'agent_session');
        const helperWorkers = runs.filter((run: any) => run.kind === 'helper_worker');
        const ctoRuns = agentSessions.filter((run: any) => run.agent_id === cto.id);
        const coderRuns = agentSessions.filter((run: any) => run.agent_id === coder.id);
        const qaRuns = agentSessions.filter((run: any) => run.agent_id === qa.id);
        expect(orchestrator).toBeTruthy();
        expect(ctoRuns.length, JSON.stringify(ctoRuns)).toBeGreaterThanOrEqual(2);
        expect(coderRuns.length, JSON.stringify(coderRuns)).toBeGreaterThanOrEqual(3);
        expect(qaRuns.length, JSON.stringify(qaRuns)).toBeGreaterThanOrEqual(2);
        expect(helperWorkers.length).toBeGreaterThanOrEqual(4);
        expect(helperWorkers.every((run: any) => run.status === 'completed')).toBeTruthy();
        expect(coderRuns.some((run: any) => run.status === 'canceled')).toBeTruthy();
        expect(coderRuns.some((run: any) => run.result_description?.includes('Forked repair completed')), JSON.stringify(coderRuns)).toBeTruthy();
        expect(qaRuns.some((run: any) => run.result_description?.includes('QA passed all final verification checks'))).toBeTruthy();

        const log = await (await request.get(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const completions = (log.requests as any[]).filter((entry) => String(entry.path).includes('/chat/completions'));
        const joined = JSON.stringify(completions);
        for (const tool of ['run_new_session', 'get_session', 'send_message_to_session', 'answer_message', 'stop_session', 'fork_session']) {
            expect(joined, `expected orchestrator tool ${tool}`).toContain(tool);
        }
        expect(joined).toContain('run_worker');
        expect(joined).toContain('worker_list');
        expect(joined).toContain('ask_task_owner');
        expect(joined).toContain('technical-spec.md');
        expect(joined).toContain('controller-state.txt');
        expect(joined).toContain('qa-fix.txt');
        expect(joined).toContain('Fork replay: completed stateful tool calls have been restored');
        expect(joined).not.toContain('ask_agent');

        const orchestratorRequests = completions.filter((entry) => entry.body?.model === 'e2e-orchestrator-model');
        const ctoRequests = completions.filter((entry) => entry.body?.model === 'e2e-cto-model');
        const coderRequests = completions.filter((entry) => entry.body?.model === 'e2e-coder-model');
        const qaRequests = completions.filter((entry) => entry.body?.model === 'e2e-qa-model');
        expect(orchestratorRequests.length).toBeGreaterThan(10);
        expect(ctoRequests.length).toBeGreaterThan(8);
        expect(coderRequests.length).toBeGreaterThan(8);
        expect(qaRequests.length).toBeGreaterThan(4);
        expect(ctoRequests.some((entry) => JSON.stringify(entry.body?.tools ?? []).includes('run_worker'))).toBeTruthy();
        expect(ctoRequests.some((entry) => JSON.stringify(entry.body?.tools ?? []).includes('answer_message'))).toBeTruthy();
        expect(coderRequests.every((entry) => !JSON.stringify(entry.body?.tools ?? []).includes('ask_human'))).toBeTruthy();
        expect(qaRequests.every((entry) => !JSON.stringify(entry.body?.tools ?? []).includes('ask_human'))).toBeTruthy();
    });
});
