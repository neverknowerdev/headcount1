import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus, waitForComment } from '../helpers/wait-for';

const env = loadE2EEnv();

// Use serial mode because subsequent tests depend on state created by the first
test.describe.serial('Paperclip2 App', () => {
    test('can go through onboarding, create project, and test full task flow', async ({ page, request }) => {
        await page.goto('/add-company');
        await expect(page.getByText('Create a Workspace')).toBeVisible({ timeout: 30000 });
        await page.fill('input[placeholder="Acme Corp"]', 'Playwright Inc');
        await page.fill('input[placeholder="acme"]', 'pw-inc');
        await page.click('button:has-text("Next Step")');

        // Step 2: Setup LLM Provider — point at the local mock provider server
        await expect(page.getByText('Setup LLM Provider')).toBeVisible();
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

        // Verify CEO agent settings initialized correctly
        await page.goto('/companies/pw-inc/agents/1');
        await page.click('button:has-text("Settings")');
        await expect(page.locator('select').nth(0)).toHaveValue("1"); // Provider
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
        await page.locator('label:has-text("Sprint") + select').selectOption({ label: 'E2E Sprint' });
        await page.click('button:has-text("Create Task")');

        // Get the newly created task ID for later waits
        const listRes = await request.get('/api/tasks?company_id=' + String(await getCompanyId(request, 'pw-inc')));
        const tasks = await listRes.json();
        const task = tasks.find((t: any) => t.title === 'Write E2E Tests');
        expect(task).toBeTruthy();
        const taskId = task.id;

        // Assign agent and move to "To Do" — this triggers the engine
        await page.click('text=Write E2E Tests');
        await expect(page.getByText('Task PW-INC-1')).toBeVisible();
        await page.locator('label:has-text("Assignee") + select').selectOption({ label: 'E2E Agent' });
        await page.locator('label:has-text("Status") + select').selectOption({ label: 'To Do' });
        await page.click('button:has-text("Save Task")');

        // The engine (real opencode + mock provider) will now run and the mock
        // provider will respond with a tool call to update_task_status, which
        // should move the task to "in-review". Wait for that real outcome.
        await waitForTaskStatus(request, taskId, 'in-review', 60_000);

        // Wait for the comment created by the agent run
        await waitForComment('http://localhost:8080', taskId, 60_000);

        // Verify run log file exists on filesystem
        const runsRes = await request.get(`/api/tasks/${taskId}/runs`);
        expect(runsRes.ok()).toBeTruthy();
        const runs = await runsRes.json();
        expect(runs.length).toBeGreaterThan(0);
        const run = runs[0];
        const basePath = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');
        const logFile = path.join(basePath, 'data', 'pw-inc', 'logs', String(taskId), `run-${run.id}.log`);
        expect(fs.existsSync(logFile)).toBeTruthy();
        const logContent = fs.readFileSync(logFile, 'utf8');
        expect(logContent).toContain('LLM Request');
        expect(logContent).toContain('LLM Response');

        // Re-open the task to verify both the user comment and the agent comment are visible
        await page.goto('/companies/pw-inc/tasks');
        await page.click('text=Write E2E Tests');
        await page.fill('input[placeholder="Add a comment..."]', 'Let us see if the agent works');
        await page.locator('.fixed.inset-0').locator('form').filter({ has: page.locator('input[placeholder="Add a comment..."]') }).locator('button[type="submit"]').click();
        await expect(page.getByText('Let us see if the agent works')).toBeVisible();

        // Verify Agent Run Logs
        await expect(page.getByText(/Run #\d/)).toBeVisible();
        await page.click('summary:has-text("Run #")');

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

    test('can add a second company using existing provider', async ({ page }) => {
        // Navigate to dashboard
        await page.goto('/companies/nw');

        // Wait for dashboard and company switcher to load
        await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible({ timeout: 10000 });

        // Click the Add Workspace button
        await page.click('button[title="Add Workspace"]');

        // Step 1: Create second Company
        await expect(page.getByText('Add Workspace')).toBeVisible();
        await page.fill('input[placeholder="Acme Corp"]', 'Second Company');
        await page.fill('input[placeholder="acme"]', 'second-co');
        await page.click('button:has-text("Next Step")');

        // Step 2: Use existing Provider
        await expect(page.getByText('Please select an existing LLM Provider')).toBeVisible();
        await page.click('button:has-text("Next Step")');

        // Step 3: Hire new CEO
        await expect(page.getByText('Hire your CEO')).toBeVisible();
        await page.fill('input[type="text"]', 'Second CEO');
        await page.click('button:has-text("Finish & Launch")');

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
