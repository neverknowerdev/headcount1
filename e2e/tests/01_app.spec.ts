import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus, waitForComment } from '../helpers/wait-for';
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

        // Step 3: Hire CEO. The form will auto-redirect to /companies/pw-inc once
        // it has created the provider + company + agent. Wait for that URL so we
        // don't race against the in-flight POSTs.
        await expect(page.getByText('Hire your CEO')).toBeVisible();
        await page.fill('input[type="text"]', 'E2E Agent');
        await page.click('button:has-text("Finish & Launch")');
        await page.waitForURL('**/companies/pw-inc', { timeout: 30_000 });

        // Main App View
        await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible({ timeout: 10_000 });
        await expect(page.getByText('System created workspace')).toBeVisible();

        // Verify CEO agent settings initialized correctly. The provider ID is
        // looked up dynamically — builtin providers (OpenRouter, OpenCode
        // Zen) are seeded first, so "Main Provider" isn't necessarily ID 1.
        const providers = await (await request.get('/api/providers')).json();
        // The onboarding UI has used both "Main Provider" and "Custom
        // Provider" labels over time. Identify the provider by the mock
        // endpoint first, falling back to the historical name for older
        // server responses.
        const mainProvider = providers.find((p: any) => p.base_url === env.E2E_MOCK_PROVIDER_URL)
            ?? providers.find((p: any) => p.name === 'Main Provider');
        expect(mainProvider).toBeDefined();

        await page.goto('/companies/pw-inc/agents/1');
        await page.click('button:has-text("Settings")');
        await expect(page.locator('select').nth(0)).toHaveValue(`provider:${mainProvider.id}`); // Provider or Model Group
        await expect(page.locator('select').nth(1)).toHaveValue("e2e-mock-model"); // Model
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

        // Assign agent and move to "To Do" — this triggers the engine
        // Navigate directly using the task id returned by the API. The board
        // card click is intentionally asynchronous and can race the task page
        // loading before its form controls are available.
        await page.goto(`/companies/pw-inc/tasks/${taskId}`);
        await expect(page).toHaveURL(new RegExp(`/companies/pw-inc/tasks/${taskId}$`));
        await expect(page.getByText('PW-INC-1')).toBeVisible();
        await expect(page.getByLabel('Assignee')).toBeVisible();
        await page.getByLabel('Assignee').selectOption({ label: 'E2E Agent' });
        await page.getByLabel('Status').selectOption({ label: 'To Do' });
        await page.click('button:has-text("Save Task")');

        // The native engine + mock provider will now run and the mock provider
        // will respond with a tool call to finish_task, moving the task
        // to "in-review". Wait for that real outcome.
        await waitForTaskStatus(request, taskId, 'in-review', 90_000);

        // Wait for the comment created by the agent run
        await waitForComment(process.env.E2E_BASE_URL || 'http://localhost:8080', taskId, 60_000);

        // Verify run log file exists on filesystem
        const runsRes = await request.get(`/api/tasks/${taskId}/runs`);
        expect(runsRes.ok()).toBeTruthy();
        const runs = await runsRes.json();
        expect(runs.length).toBeGreaterThan(0);
        // The mandatory orchestration flow creates a root orchestrator plus
        // one or more child agent sessions. Logs are grouped by the root run,
        // so select that root rather than assuming the newest row is the log
        // directory owner.
        const run = runs.find((candidate: any) => candidate.kind === 'task_orchestrator') ?? runs[runs.length - 1];
        const basePath = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');
        // Session-based JSONL layout: logs are grouped per main run in
        // logs/{company}/{taskId}/run-{id}/, the root session writing
        // main.jsonl (one JSON object per line).
        const logFile = path.join(basePath, 'logs', 'pw-inc', String(taskId), `run-${run.id}`, 'main.jsonl');
        expect(fs.existsSync(logFile)).toBeTruthy();
        const logEntries = fs.readFileSync(logFile, 'utf8').split('\n').filter(l => l.trim()).map(l => JSON.parse(l));
        expect(logEntries.some(e => e.type === 'request')).toBeTruthy();
        expect(logEntries.some(e => e.type === 'response')).toBeTruthy();

        // Re-open the task to verify both the user comment and the agent comment
        // are visible. The task run is terminal at this point, but older
        // databases can briefly retain task.run_id after the run row is already
        // completed; create this non-agent comment through the API so that UI
        // verification does not turn that bookkeeping race into a 120s retry.
        const commentResponse = await request.post('/api/comments', {
            data: {
                task_id: taskId,
                author_type: 'human',
                content: 'Let us see if the agent works',
                run_agent: false,
            },
        });
        expect(commentResponse.ok()).toBeTruthy();

        // The comments editor lives in the task modal. Wait for the board
        // card to be rendered and click the exact title so a stale/virtualized
        // element cannot leave the page on the list without opening it.
        await page.goto('/companies/pw-inc/tasks');
        const taskCard = page.getByText('Write E2E Tests', { exact: true }).first();
        await expect(taskCard).toBeVisible({ timeout: 10_000 });
        await taskCard.click();
        // Scope to the comments list: the text can also appear in the run-log
        // preview of the agent run this comment triggers.
        await expect(page.getByTestId('comments-list').getByText('Let us see if the agent works').first()).toBeVisible();

        // Verify Agent Run Logs
        await expect(page.getByText(/Run (#\d|[A-Z0-9-]+-[A-Z0-9]+)/).first()).toBeVisible();
        await page.locator('summary:has-text("Run ")').first().click();

        // Verify Run Logs page
        await page.keyboard.press('Escape');
        await page.goto('/companies/pw-inc/runs');
        await expect(page.getByRole('heading', { name: 'Run Logs' })).toBeVisible();
        await page.reload();
    });

    test('can edit company shortname in settings', async ({ page }) => {
        page.on('dialog', dialog => dialog.accept());
        await page.goto('/companies/pw-inc');

        // Go to settings
        await page.click('a:has-text("Settings")');
        await expect(page.getByRole('heading', { name: 'Company Settings' })).toBeVisible();

        // Edit short name
        const input = page.locator('input').first(); // the shortname input
        await input.fill('nw');
        await page.click('button:has-text("Save Settings")');

        // Ensure URL changed
        await expect(page).toHaveURL(/.*\/companies\/nw\/settings/);
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
        // already configured from the first company — the flow jumps straight to
        // the CEO step and reuses that provider.
        await expect(page.getByText('Hire your CEO')).toBeVisible();
        await page.fill('input[type="text"]', 'Second CEO');
        await page.click('button:has-text("Finish & Launch")');

        // AddCompany performs a full-page redirect after creating the CEO.
        // Wait for that redirect to settle before navigating back to the
        // original workspace; otherwise the two navigations race and Layout
        // can render the switcher from the previous company list.
        await page.waitForURL(/\/companies\/second-co(?:\/)?$/, { timeout: 10000 });

        // Main App View
        await page.goto('/companies/nw');
        await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible({ timeout: 10000 });

        // Verify we are on the second company
        const companyButtons = page.locator('button[title="Playwright Inc"], button[title="Second Company"]');
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
