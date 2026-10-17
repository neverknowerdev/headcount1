import { APIRequestContext, Page, TestInfo, expect } from '@playwright/test';
import { loadE2EEnv } from '../../helpers/env';
import { resetE2E } from '../../helpers/reset';
import type { LiveEnv } from './live-env';

export interface LiveWorkspace {
    companyId: number;
    shortName: string;
    providerId: number;
    sprintName: string;
}

/**
 * Starts from an empty instance and onboards a company through the UI with the
 * live provider, the way a new user does. Onboarding fills both the smart and
 * the cheap slot with the model it was given; the cheap slot is then pointed at
 * the cheap model, and the optional classifier is connected.
 */
export async function onboardLive(page: Page, request: APIRequestContext, live: LiveEnv, name: string, shortName: string): Promise<LiveWorkspace> {
    await resetE2E(request, loadE2EEnv().E2E_MOCK_PROVIDER_URL);

    await page.goto('/add-company');
    await expect(page.getByText('Create a Workspace')).toBeVisible({ timeout: 30_000 });
    await page.fill('input[placeholder="Acme Corp"]', name);
    await page.fill('input[placeholder="acme"]', shortName);
    await page.click('button:has-text("Next Step")');

    await expect(page.getByText('Setup LLM Provider')).toBeVisible();
    const customProvider = page.locator('button:has-text("Custom provider")');
    await customProvider.waitFor({ state: 'visible', timeout: 30_000 }).catch(() => {});
    if (await customProvider.isVisible().catch(() => false)) await customProvider.click();
    await page.fill('input[type="text"]', live.baseUrl);
    await page.fill('input[type="password"]', live.apiKey);
    await page.locator('label:has-text("Model Name") + input').fill(live.smartModel);
    await page.click('button:has-text("Test Connection")');
    // A real round trip to the provider: a wrong key or model fails here, not
    // twenty minutes into a task.
    await expect(page.getByText('Connection successful!')).toBeVisible({ timeout: 60_000 });
    await page.click('button:has-text("Next Step")');

    await expect(page.getByText('Launch workspace')).toBeVisible();
    await page.click('button:has-text("Create workspace")');
    await page.waitForURL(`**/companies/${shortName}`, { timeout: 30_000 });

    const providers = await (await request.get('/api/providers')).json();
    const provider = (providers as any[]).find((p) => p.provider_type !== 'typesafe' && String(p.base_url || '').startsWith(live.baseUrl.replace(/\/+$/, '')));
    expect(provider, 'the onboarded provider').toBeDefined();
    const companies = await (await request.get('/api/companies')).json();
    const company = (companies as any[]).find((c) => c.short_name === shortName);
    expect(company, 'the onboarded company').toBeDefined();

    const cheap = await request.put('/api/default-model-settings/cheap', { data: { provider_id: provider.id, model: live.cheapModel } });
    expect(cheap.ok(), await cheap.text()).toBeTruthy();
    const commit = await request.put('/api/default-model-settings/commit_messages', { data: { provider_id: provider.id, model: live.cheapModel } });
    expect(commit.ok(), await commit.text()).toBeTruthy();

    if (live.typesafeKey) {
        const typesafe = await request.post('/api/providers', {
            data: { name: 'TypeSafe', base_url: 'https://api.typesafe.ai/v1', api_key: live.typesafeKey, provider_type: 'typesafe', default_model: live.classifierModel, supported_models: live.classifierModel },
        });
        expect(typesafe.ok(), await typesafe.text()).toBeTruthy();
        const classifier = await request.put('/api/default-model-settings/classifier', { data: { provider_id: (await typesafe.json()).id, model: live.classifierModel } });
        expect(classifier.ok(), await classifier.text()).toBeTruthy();
    }

    const sprintName = 'Live Sprint';
    const sprint = await request.post('/api/sprints', {
        data: { company_id: company.id, name: sprintName, start_date: '2026-01-01T00:00:00Z', end_date: '2027-12-31T00:00:00Z' },
    });
    expect(sprint.ok(), await sprint.text()).toBeTruthy();

    return { companyId: company.id, shortName, providerId: provider.id, sprintName };
}

/** Creates a project on a non-GitHub repository from the Projects page. */
export async function createProjectInUI(page: Page, request: APIRequestContext, ws: LiveWorkspace, name: string, repositoryUrl: string): Promise<any> {
    await page.goto(`/companies/${ws.shortName}/projects`);
    await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();
    await page.click('button:has-text("Create Project")');
    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await dialog.locator('input').first().fill(name);
    await dialog.getByRole('button', { name: 'Use a non-GitHub repository or SSH URL instead' }).click();
    await dialog.locator('input[placeholder="git@github.com:user/repo.git"]').fill(repositoryUrl);
    await dialog.getByRole('button', { name: 'Create' }).click();
    await expect(dialog).toHaveCount(0, { timeout: 60_000 });

    let project: any;
    await expect.poll(async () => {
        const res = await request.get(`/api/projects?company_id=${ws.companyId}`);
        if (!res.ok()) return false;
        project = (await res.json() as any[]).find((p) => p.name === name);
        return Boolean(project);
    }, { timeout: 15_000, message: `project ${name} should be created` }).toBe(true);
    expect(project.repository_url).toBe(repositoryUrl);
    return project;
}

export interface TaskFields {
    title: string;
    description: string;
    type: 'research' | 'coding' | 'review' | 'general';
    project?: string;
}

/** Creates a task with the board's New Task form and returns it as the API sees it. */
export async function createTaskInUI(page: Page, request: APIRequestContext, ws: LiveWorkspace, fields: TaskFields): Promise<any> {
    await page.goto(`/companies/${ws.shortName}/tasks`);
    await page.click('button:has-text("New Task")');
    await page.fill('input[placeholder="Task title"]', fields.title);
    await page.fill('textarea[placeholder="Task details..."]', fields.description);
    await page.getByLabel('Type', { exact: true }).selectOption(fields.type);
    if (fields.project) {
        await page.locator('label:text-is("Project") + select').selectOption({ label: fields.project });
    }
    await page.getByLabel('Sprint', { exact: true }).selectOption({ label: ws.sprintName });
    await page.click('button:has-text("Create Task")');

    let task: any;
    await expect.poll(async () => {
        const res = await request.get(`/api/tasks?company_id=${ws.companyId}`);
        if (!res.ok()) return false;
        task = (await res.json() as any[]).find((t) => t.title === fields.title && !t.parent_id);
        return Boolean(task);
    }, { timeout: 15_000, message: 'the new task should appear in the task list' }).toBe(true);
    expect(task.task_type).toBe(fields.type);
    return task;
}

/** Opens the task page and presses Start. */
export async function startTaskInUI(page: Page, ws: LiveWorkspace, taskId: number): Promise<void> {
    await page.goto(`/companies/${ws.shortName}/tasks/${taskId}`);
    await expect(page.getByTestId('task-status-label')).toHaveText('Backlog');
    await page.getByTestId('task-start').click();
    await expect(page.getByTestId('task-status-label')).not.toHaveText('Backlog', { timeout: 30_000 });
}

/**
 * Waits for a real task to come to rest. Unlike the mock suite this polls
 * slowly and reports the journal on failure, since a live run is the only
 * record of what the models did.
 */
export async function waitForRest(request: APIRequestContext, taskId: number, timeoutMs: number, accept: (task: any) => boolean = (t) => t.status === 'in-review'): Promise<any> {
    const deadline = Date.now() + timeoutMs;
    let last: any = null;
    while (Date.now() < deadline) {
        try {
            const res = await request.get(`/api/tasks/${taskId}`, { timeout: 10_000 });
            if (res.ok()) {
                last = await res.json();
                if (accept(last)) return last;
                if (['failed', 'canceled'].includes(last.status)) break;
            }
        } catch { /* keep polling */ }
        await new Promise((resolve) => setTimeout(resolve, 3_000));
    }
    throw new Error(`task ${taskId} did not come to rest as expected: status=${last?.status} phase=${last?.phase} ` +
        `waiting_on=${last?.waiting_on} detail=${last?.wait_detail} reason=${last?.result_reason}\n` +
        `summary=${last?.result_summary}\n${await journalText(request, taskId)}`);
}

export async function journalText(request: APIRequestContext, taskId: number): Promise<string> {
    try {
        const res = await request.get(`/api/tasks/${taskId}/steps`, { timeout: 10_000 });
        const steps = (await res.json()) as any[];
        return 'Journal:\n' + steps.map((s) => `  ${s.kind} ${s.phase} ${s.tool_name || ''} ${(s.result || s.error || '').slice(0, 300)}`.trimEnd()).join('\n');
    } catch (err) {
        return `Journal unavailable: ${(err as Error).message}`;
    }
}

/**
 * Attaches what a live run did to the test report: the task tree, the
 * journal, the errors and the bill. Called after every live spec, pass or fail.
 */
export async function attachRunReport(request: APIRequestContext, testInfo: TestInfo, taskId: number | undefined): Promise<void> {
    if (!taskId) return;
    const get = async (path: string) => {
        try {
            const res = await request.get(path, { timeout: 10_000 });
            return res.ok() ? await res.json() : { error: `${res.status()} ${await res.text()}` };
        } catch (err) {
            return { error: (err as Error).message };
        }
    };
    const report = {
        task: await get(`/api/tasks/${taskId}`),
        tree: await get(`/api/tasks/${taskId}/tree`),
        usage: await get(`/api/tasks/${taskId}/usage`),
        errors: await get(`/api/tasks/${taskId}/errors`),
        decisions: await get(`/api/tasks/${taskId}/decisions?subtree=true`),
    };
    await testInfo.attach('run-report.json', { body: JSON.stringify(report, null, 2), contentType: 'application/json' });
    await testInfo.attach('journal.txt', { body: await journalText(request, taskId), contentType: 'text/plain' });
    const totals = (report.usage as any)?.totals;
    if (totals) {
        testInfo.annotations.push({ type: 'usage', description: `${totals.calls} calls, ${totals.prompt_tokens} prompt + ${totals.completion_tokens} completion tokens` });
    }
}

/** Every model call landed in the ledger with real token counts. */
export async function expectRealUsage(request: APIRequestContext, taskId: number): Promise<any> {
    const usage = await (await request.get(`/api/tasks/${taskId}/usage`)).json();
    expect(usage.totals.calls, 'model calls in the ledger').toBeGreaterThan(0);
    expect(usage.totals.prompt_tokens, 'prompt tokens reported by the provider').toBeGreaterThan(0);
    expect(usage.totals.completion_tokens, 'completion tokens reported by the provider').toBeGreaterThan(0);
    return usage;
}
