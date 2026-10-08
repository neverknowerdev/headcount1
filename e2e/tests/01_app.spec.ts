import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';
import { getDecisions, getSteps, getTree, getUsage, mockLog } from '../helpers/workflow';
import { resetE2E } from '../helpers/reset';

const env = loadE2EEnv();

// Use serial mode because subsequent tests depend on state created by the first
test.describe.serial('Headcount1 App', () => {
    test.beforeAll(async ({ request }) => {
        // Clean up any filesystem state left by a failed previous attempt.
        // In serial mode, beforeAll re-runs on retry, so this prevents
        // data/pw-inc/ (and nw, second-co) from causing the app to skip
        // onboarding and redirect to an existing company on the retry run.
        const headcount1Base = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');
        for (const shortName of ['pw-inc', 'nw', 'second-co']) {
            for (const root of ['repos', 'workspace', 'artifacts', 'logs', 'skills']) {
                const fullPath = path.join(headcount1Base, root, shortName);
                if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
            }
        }
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test('can go through onboarding, create project, and test full task flow', async ({ page, request }) => {
        await page.goto('/add-company');
        await expect(page.getByText('Create a Workspace')).toBeVisible({ timeout: 30000 });
        await page.fill('input[placeholder="Acme Corp"]', 'Playwright Inc');
        await page.fill('input[placeholder="acme"]', 'pw-inc');
        await page.click('button:has-text("Next Step")');

        // Step 2: Setup LLM Provider. The default onboarding path offers the
        // builtin free providers (OpenRouter / OpenCode Zen); switch to the
        // custom-provider form to point at the local mock provider server.
        await expect(page.getByText('Setup LLM Provider')).toBeVisible();
        // The onboarding lands on the free-providers picker (builtin OpenRouter /
        // OpenCode Zen cards plus a "Custom provider" escape hatch). Switch to the
        // custom form; if no builtins are available the form is already shown, so
        // the escape hatch is optional.
        const customProviderCard = page.locator('button:has-text("Custom provider")');
        await customProviderCard.waitFor({ state: 'visible', timeout: 30_000 }).catch(() => {});
        if (await customProviderCard.isVisible().catch(() => false)) {
            await customProviderCard.click();
        }
        await page.fill('input[type="text"]', env.E2E_MOCK_PROVIDER_URL);
        await page.fill('input[type="password"]', 'test-api-key');
        await page.locator('label:has-text("Model Name") + input').fill('e2e-mock-model');
        await page.click('button:has-text("Test Connection")');
        await expect(page.getByText('Connection successful!')).toBeVisible();
        await page.click('button:has-text("Next Step")');

        // Step 3: launch the workspace. The server creates the complete protected
        // built-in agent catalog alongside the company.
        await expect(page.getByText('Launch workspace')).toBeVisible();
        await page.click('button:has-text("Create workspace")');
        await page.waitForURL('**/companies/pw-inc', { timeout: 30_000 });

        // Main App View
        await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible({ timeout: 10_000 });
        await expect(page.getByText('System created workspace')).toBeVisible();

        // The model picked during onboarding became the smart and the cheap
        // default, so the first task can run without any further setup.
        const providers = await (await request.get('/api/providers')).json();
        // The stored URL may be normalised (a /v1 suffix), so match by prefix.
        const mainProvider = providers.find((p: any) => String(p.base_url || '').startsWith(env.E2E_MOCK_PROVIDER_URL));
        expect(mainProvider).toBeDefined();
        const slots = await (await request.get('/api/default-model-settings')).json();
        for (const purpose of ['smart', 'cheap']) {
            const slot = slots.find((s: any) => s.purpose === purpose);
            expect(slot?.provider_id, `${purpose} tier`).toBe(mainProvider.id);
            expect(slot?.model, `${purpose} tier`).toBe('e2e-mock-model');
        }

        // An agent is a role: a one-line prompt the user can extend, and
        // nothing about models, tools or MCP servers.
        await page.goto('/companies/pw-inc/agents/1');
        await expect(page.getByTestId('agent-role')).toBeVisible();
        await expect(page.getByLabel('Prompt')).toHaveValue('You are the CEO agent.');
        await expect(page.getByText('Tools & MCPs')).toHaveCount(0);
        await expect(page.getByText('LLM Provider or Model Group')).toHaveCount(0);
        await page.goto('/companies/pw-inc');

        // Navigate to Projects
        await page.click('a:has-text("Projects")');
        await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();

        // Create Project
        await page.click('button:has-text("Create Project")');
        await expect(page.getByRole('dialog')).toBeVisible();
        await page.getByRole('dialog').locator('input').first().fill('Project Alpha');
        await expect(page.getByRole('dialog').locator('input').nth(1)).toHaveValue('pw-inc/project-alpha');
        await page.getByRole('dialog').getByRole('button', { name: 'Create' }).click();
        await expect(page.getByText('Project Alpha').first()).toBeVisible({ timeout: 10000 });

        // Navigate to Tasks
        await page.goto('/companies/pw-inc/tasks');

        // Add a Sprint
        await page.click('button:has-text("Manage Sprints")');
        await expect(page.getByRole('heading', { name: 'Manage Sprints' })).toBeVisible();
        await page.click('button:has-text("Create Sprint")');
        await page.fill('input[placeholder="e.g. Sprint 1"]', 'E2E Sprint');
        await page.fill('input[type="date"]', '2024-01-01'); // Start
        await page.locator('input[type="date"]').nth(1).fill('2024-01-14'); // End
        await page.getByRole('dialog').getByRole('button', { name: 'Create' }).click();
        await expect(page.getByText('E2E Sprint')).toBeVisible();

        await page.goto('/companies/pw-inc/tasks');

        // Add Task
        await page.click('button:has-text("New Task")');
        await page.fill('input[placeholder="Task title"]', 'Write E2E Tests');
        await page.getByLabel('Type').selectOption('research');
        await page.getByLabel('Sprint').selectOption({ label: 'E2E Sprint' });
        await page.click('button:has-text("Create Task")');

        // The UI navigation can render before the task POST is visible to a
        // subsequent list query. Poll the API for the task created above.
        let task: any;
        await expect.poll(async () => {
            const listRes = await request.get('/api/tasks?company_id=' + String(await getCompanyId(request, 'pw-inc')));
            if (!listRes.ok()) return undefined;
            const tasks = await listRes.json();
            task = (tasks as any[]).find((t: any) => t.title === 'Write E2E Tests');
            return Boolean(task);
        }, { timeout: 15_000, message: 'created task should appear in the task list' }).toBe(true);
        const taskId = task.id;

        // Start the task from its page. Navigate directly using the task id
        // returned by the API: the board card click is asynchronous and can
        // race the task page loading.
        await page.goto(`/companies/pw-inc/tasks/${taskId}`);
        await expect(page).toHaveURL(new RegExp(`/companies/pw-inc/tasks/${taskId}$`));
        await expect(page.getByText('PW-INC-1')).toBeVisible();
        await expect(page.getByTestId('task-status-label')).toHaveText('Backlog');
        // Calls made before the task started (the connection test) are not its bill.
        const servedBefore = (await mockLog()).completionsAnswered;
        await page.getByTestId('task-start').click();

        // With no scenario set the mock provider simply gets on with it: the
        // smart model refines, plans one subtask and verifies; the cheap model
        // does the subtask. The task then waits in review for a person.
        await waitForTaskStatus(request, taskId, 'in-review', 90_000);

        // The workflow left its record: phases, smart calls, the subtask.
        const steps = await getSteps(request, taskId);
        expect(steps[0].kind).toBe('workflow_started');
        expect(steps.filter((step: any) => step.kind === 'smart_call').map((step: any) => step.tool_name))
            .toEqual(['finish_refinement', 'create_tasks', 'finish_verification']);
        expect(steps[steps.length - 1].kind).toBe('finished');
        const tree = await getTree(request, taskId);
        expect(tree).toHaveLength(2);
        const subtask = tree[1];
        expect(subtask.status).toBe('done');
        expect(subtask.mode).toBe('direct');
        expect((await getDecisions(request, taskId)).length).toBeGreaterThanOrEqual(3);

        // The subtask's executor session logged to the task tree's folder.
        const runsRes = await request.get(`/api/tasks/${subtask.id}/runs`);
        expect(runsRes.ok()).toBeTruthy();
        const runs = await runsRes.json();
        expect(runs).toHaveLength(1);
        const basePath = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');
        const taskLogs = path.join(basePath, 'logs', 'pw-inc', String(taskId));
        const logFile = path.join(taskLogs, `task-${subtask.id}`, `run-${runs[0].id}.jsonl`);
        expect(fs.existsSync(logFile), logFile).toBeTruthy();
        const logEntries = fs.readFileSync(logFile, 'utf8').split('\n').filter(l => l.trim()).map(l => JSON.parse(l));
        expect(logEntries.some(e => e.type === 'request')).toBeTruthy();
        expect(logEntries.some(e => e.type === 'response')).toBeTruthy();
        // The task's own journal and decisions are mirrored beside it.
        expect(fs.existsSync(path.join(taskLogs, `task-${taskId}`, 'task.jsonl'))).toBeTruthy();
        expect(fs.existsSync(path.join(taskLogs, `task-${taskId}`, 'decisions.jsonl'))).toBeTruthy();

        // Every model call is in the usage ledger, and the ledger agrees with
        // what the provider served.
        const usage = await getUsage(request, taskId);
        const served = (await mockLog()).completionsAnswered - servedBefore;
        expect(served).toBe(4); // refine, plan, the subtask's one turn, verify
        expect(usage.totals.calls).toBe(served);
        expect(usage.totals.prompt_tokens).toBe(served * 100);

        // A person comments on the finished task without running it again.
        const commentResponse = await request.post('/api/comments', {
            data: { task_id: taskId, author_type: 'human', content: 'Looks right to me', run_agent: false },
        });
        expect(commentResponse.ok()).toBeTruthy();

        // The task page shows all of it. Open it from the board.
        await page.goto('/companies/pw-inc/tasks');
        const taskCard = page.getByText('Write E2E Tests', { exact: true }).first();
        await expect(taskCard).toBeVisible({ timeout: 10_000 });
        await taskCard.click();
        await expect(page.getByTestId('task-status-label')).toHaveText('In review');
        await expect(page.getByTestId('task-result')).toContainText('E2E task completed and ready for review.');
        await expect(page.getByTestId('comments-list').getByText('Looks right to me').first()).toBeVisible();

        await page.getByTestId('task-tab-workflow').click();
        await expect(page.getByTestId('subtask-tree')).toContainText('Do the work');
        await expect(page.getByTestId('task-journal')).toContainText('finish_refinement');
        // A smart call opens to the full prompt it was given.
        await page.getByTestId('journal-step-smart_call').first().click();
        await expect(page.getByTestId('smart-prompt').first()).toContainText('Write E2E Tests');

        await page.getByTestId('task-tab-decisions').click();
        await expect(page.getByTestId('decision-tree')).toContainText('Take the task as written');

        await page.getByTestId('task-tab-usage').click();
        await expect(page.getByTestId('usage-by-phase')).toContainText('Refine');

        // The executor session is on the Run Logs page.
        await page.goto('/companies/pw-inc/runs');
        await expect(page.getByRole('heading', { name: 'Run Logs' })).toBeVisible();
        await expect(page.getByTestId('run-card')).toHaveCount(1);

        // Accepting the result is the person's move.
        const accept = await request.put(`/api/tasks/${taskId}`, { data: { status: 'done' } });
        expect(accept.ok(), await accept.text()).toBeTruthy();
        await waitForTaskStatus(request, taskId, 'done', 10_000);
    });

    test('can edit company name and shortname in settings', async ({ page, request }) => {
        page.on('dialog', dialog => dialog.accept());
        const existingCompanies = await (await request.get('/api/companies')).json();
        if (!(existingCompanies as any[]).some((company) => company.short_name === 'pw-inc')) {
            const createCompany = await request.post('/api/companies', {
                data: { name: 'Playwright Inc', short_name: 'pw-inc', color: '#4f46e5' },
            });
            expect(createCompany.ok(), await createCompany.text()).toBeTruthy();
        }
        await page.goto('/companies/pw-inc');

        // Go to settings
        await page.click('a:has-text("Settings")');
        await expect(page.getByRole('heading', { name: 'Company Settings' })).toBeVisible();
        // The settings inputs hydrate from the company store after navigation.
        // Wait for that initial state so the async effect cannot overwrite the
        // edits while the form is being filled.
        await expect(page.getByLabel('Company Full Name')).toHaveValue('Playwright Inc');
        await expect(page.getByLabel('Company Short Name')).toHaveValue('pw-inc');

        // Edit the full company name and short name together.
        await page.getByLabel('Company Full Name').fill('Playwright Incorporated');
        await page.getByLabel('Company Short Name').fill('nw');
        const updateCompany = page.waitForResponse(response =>
            response.request().method() === 'PUT' && /\/api\/companies\/\d+$/.test(response.url())
        );
        await page.click('button:has-text("Save Settings")');
        await expect((await updateCompany).ok()).toBeTruthy();

        // Ensure URL changed
        await expect(page).toHaveURL(/.*\/companies\/nw\/settings/);
        await expect(page.getByLabel('Company Full Name')).toHaveValue('Playwright Incorporated');

        const companies = await (await request.get('/api/companies')).json();
        const company = companies.find((c: any) => c.short_name === 'nw');
        expect(company?.name).toBe('Playwright Incorporated');

        await page.reload();
        await expect(page.getByLabel('Company Full Name')).toHaveValue('Playwright Incorporated');
    });

    test('can add a second company reusing the existing provider', async ({ page }) => {
        // Navigate to dashboard
        await page.goto('/companies/nw');

        // Wait for dashboard and company switcher to load
        await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible({ timeout: 10000 });

        // Opening the form triggers GET /api/providers; wait for it so the
        // already-configured provider is detected before we advance and the
        // provider-setup step is auto-skipped.
        const providersLoaded = page.waitForResponse(
            r => r.url().includes('/api/providers') && r.request().method() === 'GET'
        );
        await page.click('button[title="Add Workspace"]');
        await providersLoaded;

        // Step 1: Create second Company
        await expect(page.getByText('Add Workspace')).toBeVisible();
        await page.fill('input[placeholder="Acme Corp"]', 'Second Company');
        await page.fill('input[placeholder="acme"]', 'second-co');
        await page.click('button:has-text("Next Step")');

        // Provider setup (step 2) is skipped automatically because a provider is
        // already configured from the first company — the complete built-in
        // catalog is seeded with that provider.
        await expect(page.getByText('Launch workspace')).toBeVisible();
        await page.click('button:has-text("Create workspace")');

        // AddCompany performs a full-page redirect after creating the CEO.
        // Wait for that redirect to settle before navigating back to the
        // original workspace; otherwise the two navigations race and Layout
        // can render the switcher from the previous company list.
        await page.waitForURL(/\/companies\/second-co(?:\/)?$/, { timeout: 10000 });

        // Main App View
        await page.goto('/companies/nw');
        await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible({ timeout: 10000 });

        // Verify we are on the second company
        const companyButtons = page.locator('button[title="Playwright Incorporated"], button[title="Second Company"]');
        await expect(companyButtons).toHaveCount(2);
    });
});

async function getCompanyId(request: any, shortName: string): Promise<number> {
    const res = await request.get('/api/companies');
    const companies = await res.json();
    const c = companies.find((c: any) => c.short_name === shortName);
    if (!c) throw new Error(`getCompanyId: no company with short_name=${shortName}`);
    return c.id;
}
