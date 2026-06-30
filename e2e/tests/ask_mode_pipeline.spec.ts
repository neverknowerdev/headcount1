/**
 * E2E tests for the AskMode 3-stage orchestration pipeline
 * (refinement → implementation → testing).
 *
 * Each test configures the mock LLM provider with a scenario (an ordered list
 * of tool calls / text replies) that drives a SmartPlanner orchestrator
 * through refinement, a Coder implementation phase, and a Tester phase, then
 * inspects the resulting task fields, sessions, and the requests the mock
 * provider received to verify the pipeline actually wired things together
 * end-to-end (not just that the task eventually reached "in-review").
 */

import { test, expect } from '@playwright/test';
import type { APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';
import type { ScenarioEntry } from '../fixtures/mock-provider-server';

const env = loadE2EEnv();

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Configure the mock LLM provider to run a specific scenario. */
async function setScenario(request: APIRequestContext, entries: ScenarioEntry[]): Promise<void> {
    const res = await request.post(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
        data: { entries },
    });
    expect(res.ok(), `set-scenario failed: ${await res.text()}`).toBeTruthy();
}

/** Reset the mock LLM provider to default mode and clear recorded requests. */
async function resetMockProvider(request: APIRequestContext): Promise<void> {
    await request.post(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`);
}

/** Return all requests the mock LLM provider has received. */
async function getMockRequests(request: APIRequestContext): Promise<any[]> {
    const res = await request.get(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`);
    const data = await res.json();
    return data.requests ?? [];
}

/**
 * Set up a fresh company + LLM provider + agent via the REST API.
 * Returns the IDs needed to create and run tasks.
 */
async function setupWorkspace(request: APIRequestContext, shortName: string): Promise<{
    companyId: number;
    agentId: number;
    sprintId: number;
}> {
    const compRes = await request.post('/api/companies', {
        data: { name: `AskMode Test Co (${shortName})`, short_name: shortName, color: '#a855f7' },
    });
    expect(compRes.ok(), `create company: ${await compRes.text()}`).toBeTruthy();
    const company = await compRes.json();

    const sprintRes = await request.post('/api/sprints', {
        data: {
            company_id: company.id,
            name: 'Test Sprint',
            goal: '',
            start_date: '2024-01-01T00:00:00Z',
            end_date: '2024-12-31T00:00:00Z',
        },
    });
    expect(sprintRes.ok(), `create sprint: ${await sprintRes.text()}`).toBeTruthy();
    const sprint = await sprintRes.json();

    const provRes = await request.post('/api/providers', {
        data: {
            name: 'Mock Provider',
            base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key',
            provider_type: 'openai',
            default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model',
        },
    });
    expect(provRes.ok(), `create provider: ${await provRes.text()}`).toBeTruthy();
    const provider = await provRes.json();

    const agentRes = await request.post('/api/agents', {
        data: {
            company_id: company.id,
            name: 'AskMode Test Agent',
            model: 'e2e-mock-model',
            provider_id: provider.id,
        },
    });
    expect(agentRes.ok(), `create agent: ${await agentRes.text()}`).toBeTruthy();
    const agent = await agentRes.json();

    return { companyId: company.id, agentId: agent.id, sprintId: sprint.id };
}

/**
 * Create a task in backlog, optionally with a task_type, then set status →
 * 'to-do' which triggers the SmartPlanner orchestration pipeline. Returns taskId.
 */
async function runTask(
    request: APIRequestContext,
    companyId: number,
    agentId: number,
    sprintId: number,
    title: string,
    taskType?: string,
): Promise<number> {
    const taskRes = await request.post('/api/tasks', {
        data: {
            company_id: companyId,
            sprint_id: sprintId,
            title,
            task_type: taskType,
            user_input: title,
            agent_id: agentId,
        },
    });
    expect(taskRes.ok(), `create task: ${await taskRes.text()}`).toBeTruthy();
    const task = await taskRes.json();

    const upd = await request.put(`/api/tasks/${task.id}`, {
        data: { status: 'to-do', agent_id: agentId },
    });
    expect(upd.ok(), `update task status: ${await upd.text()}`).toBeTruthy();

    return task.id;
}

/** Returns every message across all requests, in request order. */
function extractAllMessages(requests: any[]): any[] {
    const all: any[] = [];
    for (const req of requests) {
        const messages: any[] = (req.body as any)?.messages ?? [];
        all.push(...messages);
    }
    return all;
}

/** Returns all tool-role message contents (the strings sent back to the LLM as tool results). */
function extractToolResults(requests: any[]): string[] {
    return extractAllMessages(requests)
        .filter((m) => m.role === 'tool' && typeof m.content === 'string')
        .map((m) => m.content);
}

/** Returns all system-role message contents seen across requests. */
function extractSystemPrompts(requests: any[]): string[] {
    return extractAllMessages(requests)
        .filter((m) => m.role === 'system' && typeof m.content === 'string')
        .map((m) => m.content);
}

interface SessionRecord {
    id: number;
    task_id: number;
    parent_session_id: number | null;
    session_type: string;
    agent_config_name: string;
    status: string;
    result_summary: string;
}

async function getTaskSessions(request: APIRequestContext, taskId: number): Promise<SessionRecord[]> {
    const res = await request.get(`/api/tasks/${taskId}/sessions`);
    expect(res.ok(), `get task sessions: ${await res.text()}`).toBeTruthy();
    return res.json();
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

test.describe.serial('AskMode pipeline: refinement → implementation → testing', () => {
    const SHORT = 'askmode-test';
    let companyId: number;
    let agentId: number;
    let sprintId: number;

    test.beforeAll(async ({ request }) => {
        // Remove any leftover folder from a prior failed run before wiping the DB,
        // so this company's directory doesn't pollute other tests' filesystem-sync
        // scans (CreateCompany writes companies/{shortName}/ to disk immediately).
        const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');
        for (const subDir of [`data/${SHORT}`, `companies/${SHORT}`]) {
            const fullPath = path.join(paperclipBase, subDir);
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }

        await request.post('/api/e2e/wipe-db');
        const ws = await setupWorkspace(request, SHORT);
        companyId = ws.companyId;
        agentId = ws.agentId;
        sprintId = ws.sprintId;
    });

    test.beforeEach(async ({ request }) => {
        await resetMockProvider(request);
    });

    test.afterAll(async () => {
        // Clean up this suite's on-disk company folder so it doesn't leak into
        // later test files' filesystem-sync scans.
        const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');
        for (const subDir of [`data/${SHORT}`, `companies/${SHORT}`]) {
            const fullPath = path.join(paperclipBase, subDir);
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }
    });

    test('full pipeline: batched research questions feed refinement, then implementation and testing complete the task', async ({ request }) => {
        await setScenario(request, [
            // 1. SmartPlanner asks two questions in a single turn — exercises batching.
            {
                tool_calls: [
                    { id: 'q1', name: 'ask_question', arguments: { question: 'What test framework does this project use?' } },
                    { id: 'q2', name: 'ask_question', arguments: { question: 'What build tool does this project use?' } },
                ],
            },
            // 2. Researcher answers both questions in one turn.
            {
                tool_call: {
                    id: 'aq1',
                    name: 'answer_question',
                    arguments: {
                        answers: [
                            { n: 1, answer: 'PIPELINE_ANSWER_FRAMEWORK_42' },
                            { n: 2, answer: 'PIPELINE_ANSWER_BUILD_77' },
                        ],
                    },
                },
            },
            // 3. SmartPlanner, now holding the answers, finishes refinement.
            {
                tool_call: {
                    id: 'fr1',
                    name: 'finish_refinement',
                    arguments: {
                        detailed_description: 'PIPELINE_DETAILED_DESCRIPTION',
                        specifications: 'PIPELINE_SPECIFICATIONS',
                        acceptance_criteria: 'PIPELINE_ACCEPTANCE_CRITERIA',
                        test_cases: '[]',
                    },
                },
            },
            // 4. Coder implements (plain text ends the Coder phase successfully).
            { text: 'Implementation complete.' },
            // 5. Tester tests (plain text ends the Tester phase successfully).
            { text: 'Testing complete.' },
        ]);

        const taskId = await runTask(request, companyId, agentId, sprintId, 'AskMode full pipeline happy path', 'tech');
        await waitForTaskStatus(request, taskId, 'in-review', 120_000);

        // The refined specification fields must have been persisted onto the task.
        const taskRes = await request.get(`/api/tasks/${taskId}`);
        const task = await taskRes.json();
        expect(task.detailed_description).toBe('PIPELINE_DETAILED_DESCRIPTION');
        expect(task.specifications).toBe('PIPELINE_SPECIFICATIONS');
        expect(task.acceptance_criteria).toBe('PIPELINE_ACCEPTANCE_CRITERIA');

        // The batched research answers must have made it back to the SmartPlanner
        // as tool results before it called finish_refinement.
        const reqs = await getMockRequests(request);
        const toolResults = extractToolResults(reqs);
        expect(toolResults).toContain('PIPELINE_ANSWER_FRAMEWORK_42');
        expect(toolResults).toContain('PIPELINE_ANSWER_BUILD_77');

        // Sessions: one orchestration, one qa-research, one implementation, one testing — all completed.
        const sessions = await getTaskSessions(request, taskId);
        const byType = (t: string) => sessions.filter((s) => s.session_type === t);
        expect(byType('orchestration')).toHaveLength(1);
        expect(byType('qa-research')).toHaveLength(1);
        expect(byType('implementation')).toHaveLength(1);
        expect(byType('testing')).toHaveLength(1);
        for (const s of sessions) {
            expect(s.status, `session ${s.id} (${s.session_type}) should be completed`).toBe('completed');
        }
    });

    test('escalation: Coder asks the orchestrator a question and continues with the answer', async ({ request }) => {
        await setScenario(request, [
            // 1. SmartPlanner finishes refinement without any research questions.
            {
                tool_call: {
                    id: 'fr2',
                    name: 'finish_refinement',
                    arguments: {
                        detailed_description: 'Escalation test task.',
                        specifications: 'Implement using the approach the orchestrator approves.',
                        acceptance_criteria: 'Works correctly.',
                        test_cases: '[]',
                    },
                },
            },
            // 2. Coder hits an ambiguity and escalates to the orchestrator.
            {
                tool_call: {
                    id: 'ato1',
                    name: 'ask_task_owner',
                    arguments: { question: 'Should I use approach A or approach B?' },
                },
            },
            // 3. The orchestrator's one-shot escalation completion — plain text answer.
            { text: 'ESCALATION_ANSWER_USE_APPROACH_B_99' },
            // 4. Coder continues with the answer and finishes.
            { text: 'Implemented using approach B as instructed.' },
            // 5. Tester finishes.
            { text: 'Tests pass.' },
        ]);

        const taskId = await runTask(request, companyId, agentId, sprintId, 'AskMode escalation round trip', 'tech');
        await waitForTaskStatus(request, taskId, 'in-review', 120_000);

        // The escalation answer must have been delivered back to the Coder as a tool result.
        const reqs = await getMockRequests(request);
        const toolResults = extractToolResults(reqs);
        expect(toolResults).toContain('ESCALATION_ANSWER_USE_APPROACH_B_99');
    });

    test('task_type "writing" routes research questions to the WritingSpecResearcher', async ({ request }) => {
        await setScenario(request, [
            // 1. SmartPlanner asks a single research question.
            {
                tool_call: {
                    id: 'q3',
                    name: 'ask_question',
                    arguments: { question: 'What tone should the documentation use?' },
                },
            },
            // 2. Researcher answers.
            {
                tool_call: {
                    id: 'aq2',
                    name: 'answer_question',
                    arguments: { answers: [{ n: 1, answer: 'WRITING_TONE_ANSWER_FORMAL_55' }] },
                },
            },
            // 3. SmartPlanner finishes refinement.
            {
                tool_call: {
                    id: 'fr3',
                    name: 'finish_refinement',
                    arguments: {
                        detailed_description: 'Writing task.',
                        specifications: 'Use a formal tone.',
                        acceptance_criteria: 'Reads well.',
                        test_cases: '[]',
                    },
                },
            },
            { text: 'Draft written.' },
            { text: 'Reviewed.' },
        ]);

        const taskId = await runTask(request, companyId, agentId, sprintId, 'AskMode writing task_type routing', 'writing');
        await waitForTaskStatus(request, taskId, 'in-review', 120_000);

        const reqs = await getMockRequests(request);
        const systemPrompts = extractSystemPrompts(reqs);

        // The researcher turn must have used the WritingSpecResearcher prompt, not the tech one.
        expect(systemPrompts.some((p) => p.includes('You are a WritingSpecResearcher'))).toBe(true);
        expect(systemPrompts.some((p) => p.includes('You are a TechSpecResearcher'))).toBe(false);
    });

    test('testing failure triggers a retry cycle; the second attempt succeeds', async ({ request }) => {
        await setScenario(request, [
            // 1. SmartPlanner finishes refinement without research.
            {
                tool_call: {
                    id: 'fr4',
                    name: 'finish_refinement',
                    arguments: {
                        detailed_description: 'Retry test task.',
                        specifications: 'Must pass tests on a retry.',
                        acceptance_criteria: 'Tests pass eventually.',
                        test_cases: '[]',
                    },
                },
            },
            // 2. Coder cycle 1.
            { text: 'Implementation cycle 1 done.' },
            // 3. Tester cycle 1 reports the implementation is blocked (a bug was found).
            {
                tool_call: {
                    id: 'ft1',
                    name: 'finish_task',
                    arguments: { task_status: 'blocked', finish_status: 'RETRY_CYCLE1_BUG_FOUND' },
                },
            },
            // 4. Coder cycle 2 fixes the bug.
            { text: 'Implementation cycle 2 done. Bug fixed.' },
            // 5. Tester cycle 2 passes.
            { text: 'Testing cycle 2 passed.' },
        ]);

        const taskId = await runTask(request, companyId, agentId, sprintId, 'AskMode retry on test failure', 'tech');
        await waitForTaskStatus(request, taskId, 'in-review', 150_000);

        // Two testing sessions should exist: one failed (cycle 1, blocked), one completed (cycle 2).
        const sessions = await getTaskSessions(request, taskId);
        const testingSessions = sessions.filter((s) => s.session_type === 'testing');
        expect(testingSessions).toHaveLength(2);
        expect(testingSessions.filter((s) => s.status === 'failed')).toHaveLength(1);
        expect(testingSessions.filter((s) => s.status === 'completed')).toHaveLength(1);

        // Implementation ran twice too (cycle 1 and the retry cycle 2), both completing
        // normally — only the Tester reported "blocked", not the Coder.
        const implSessions = sessions.filter((s) => s.session_type === 'implementation');
        expect(implSessions).toHaveLength(2);
        for (const s of implSessions) {
            expect(s.status).toBe('completed');
        }
    });
});
