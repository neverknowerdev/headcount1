import { APIRequestContext, expect } from '@playwright/test';
import { loadE2EEnv } from './env';
import { fetchJSON } from './http';
import { postMockJSON } from './reset';
import type { ReceivedRequest, ScenarioEntry, ScenarioRule } from '../fixtures/mock-provider-server';

export const SMART_MODEL = 'e2e-smart-model';
export const CHEAP_MODEL = 'e2e-cheap-model';

/** Scripts the mock provider. Requests no rule matches are answered by its autopilot. */
export async function setScenario(rules: ScenarioRule[], append = false): Promise<void> {
    await postMockJSON(`${loadE2EEnv().E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, { rules, append });
}

export const call = (name: string, args: Record<string, unknown>): ScenarioEntry => ({ tool_call: { name, arguments: args } });

/** A decision list for a smart tool call; every state-changing tool requires one. */
export const decided = (title: string, rationale = 'Scripted by the test.') => [{ title, decision: title, rationale }];

/** The executor's closing report. */
export const finishWork = (status: 'done' | 'failed' | 'cannot_complete', summary: string, extra: Record<string, unknown> = {}): ScenarioEntry =>
    call('finish_work', { status, summary, ...extra });

export interface MockLog {
    completionsReceived: number;
    completionsAnswered: number;
    held: number;
    completions: ReceivedRequest[];
    /** Calls made to System One models. */
    systemOne: ReceivedRequest[];
}

export async function mockLog(): Promise<MockLog> {
    return fetchJSON<MockLog>(`${loadE2EEnv().E2E_MOCK_PROVIDER_URL}/__test/requests`, {}, 5_000);
}

export async function holdMock(match?: Record<string, string>): Promise<void> {
    await postMockJSON(`${loadE2EEnv().E2E_MOCK_PROVIDER_URL}/__test/hold`, { match });
}

export async function releaseMock(): Promise<void> {
    await postMockJSON(`${loadE2EEnv().E2E_MOCK_PROVIDER_URL}/__test/release`, {});
}

/** The text of a completion request's messages, for asserting what a model was shown. */
export function promptText(entry: ReceivedRequest): string {
    const messages = ((entry.body as { messages?: { content?: unknown }[] } | null)?.messages) || [];
    return messages.map((message) => (typeof message.content === 'string' ? message.content : JSON.stringify(message.content ?? ''))).join('\n');
}

export interface Workspace {
    companyId: number;
    shortName: string;
    providerId: number;
    sprintId: number;
}

/**
 * Creates a company whose smart and cheap tiers run on two different models
 * of the mock provider, with one sprint: everything a task needs to run.
 */
export async function createWorkspace(request: APIRequestContext, name: string, shortName: string): Promise<Workspace> {
    const env = loadE2EEnv();
    const providerRes = await request.post('/api/providers', {
        data: {
            name: `${name} provider`, base_url: env.E2E_MOCK_PROVIDER_URL, api_key: 'test-key', provider_type: 'openai',
            default_model: SMART_MODEL, supported_models: `${SMART_MODEL},${CHEAP_MODEL}`,
        },
    });
    expect(providerRes.ok(), await providerRes.text()).toBeTruthy();
    const provider = await providerRes.json();
    const companyRes = await request.post('/api/companies', { data: { name, short_name: shortName, provider_id: provider.id, model: SMART_MODEL } });
    expect(companyRes.ok(), await companyRes.text()).toBeTruthy();
    const company = await companyRes.json();
    for (const [purpose, model] of [['smart', SMART_MODEL], ['cheap', CHEAP_MODEL]]) {
        const res = await request.put(`/api/default-model-settings/${purpose}`, { data: { provider_id: provider.id, model } });
        expect(res.ok(), await res.text()).toBeTruthy();
    }
    const sprintRes = await request.post('/api/sprints', {
        data: { company_id: company.id, name: 'Sprint 1', start_date: '2026-01-01T00:00:00Z', end_date: '2026-12-31T00:00:00Z' },
    });
    expect(sprintRes.ok(), await sprintRes.text()).toBeTruthy();
    const sprint = await sprintRes.json();
    return { companyId: company.id, shortName, providerId: provider.id, sprintId: sprint.id };
}

export async function createTask(request: APIRequestContext, workspace: Workspace, fields: Record<string, unknown>): Promise<any> {
    const res = await request.post('/api/tasks', { data: { company_id: workspace.companyId, sprint_id: workspace.sprintId, ...fields } });
    expect(res.ok(), await res.text()).toBeTruthy();
    return res.json();
}

/** Queues a task: the workflow picks it up from to-do. */
export async function startTask(request: APIRequestContext, taskId: number): Promise<void> {
    const res = await request.put(`/api/tasks/${taskId}`, { data: { status: 'to-do' } });
    expect(res.ok(), await res.text()).toBeTruthy();
}

export async function getTask(request: APIRequestContext, taskId: number): Promise<any> {
    const res = await request.get(`/api/tasks/${taskId}`);
    expect(res.ok(), await res.text()).toBeTruthy();
    return res.json();
}

export async function getJSON(request: APIRequestContext, path: string): Promise<any> {
    const res = await request.get(path);
    expect(res.ok(), `${path}: ${await res.text()}`).toBeTruthy();
    return res.json();
}

/** A task and everything beneath it, parents first. */
export const getTree = (request: APIRequestContext, taskId: number): Promise<any[]> => getJSON(request, `/api/tasks/${taskId}/tree`);
export const getSteps = (request: APIRequestContext, taskId: number): Promise<any[]> => getJSON(request, `/api/tasks/${taskId}/steps`);
export const getDecisions = (request: APIRequestContext, taskId: number): Promise<any[]> => getJSON(request, `/api/tasks/${taskId}/decisions?subtree=true`);
export const getUsage = (request: APIRequestContext, taskId: number): Promise<any> => getJSON(request, `/api/tasks/${taskId}/usage`);

/** Waits until a task satisfies a condition, reporting its last state if it never does. */
export async function waitForTask(request: APIRequestContext, taskId: number, until: (task: any) => boolean, what: string, timeoutMs = 60_000): Promise<any> {
    const deadline = Date.now() + timeoutMs;
    let last: any = null;
    while (Date.now() < deadline) {
        try {
            const res = await request.get(`/api/tasks/${taskId}`, { timeout: 5_000 });
            if (res.ok()) {
                last = await res.json();
                if (until(last)) return last;
            }
        } catch { /* the server may be restarting; keep polling */ }
        await new Promise((resolve) => setTimeout(resolve, 250));
    }
    let journal = '';
    try {
        const steps = await getSteps(request, taskId);
        journal = steps.map((step: any) => `  ${step.kind} ${step.phase} ${step.tool_name} ${step.result || step.error}`.trimEnd()).join('\n');
    } catch { /* diagnostic only */ }
    throw new Error(
        `task ${taskId} never became ${what} within ${timeoutMs}ms. Last seen: status=${last?.status} phase=${last?.phase} ` +
        `waiting_on=${last?.waiting_on} detail=${last?.wait_detail}\nJournal:\n${journal}`,
    );
}
