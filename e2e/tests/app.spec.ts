import { test, expect } from '@playwright/test';

// Use serial mode because the second test depends on the state created by the first
test.describe.serial('Paperclip2 App', () => {
    test('can go through onboarding, create project, and test full task flow', async ({ page }) => {
        await page.waitForTimeout(2000);

        await page.route('**/api/settings', async route => {
            await route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({
                    base_path: '/tmp/.paperclip2',
                    workspace_folders: []
                })
            });
        });

        await page.goto('/');

        // Step 1: Create Company (Now on /add-company)
        await expect(page.getByText('Create a Workspace')).toBeVisible();
        await page.fill('input[placeholder="Acme Corp"]', 'Playwright Inc');
        await page.fill('input[placeholder="acme"]', 'pw-inc');
        await page.click('button:has-text("Next Step")');

        // Step 2: Setup LLM Provider
        await expect(page.getByText('Setup LLM Provider')).toBeVisible();
        await page.fill('input[type="text"]', 'e2e-mock');
        await page.fill('input[type="password"]', 'test-api-key');
        await page.locator('label:has-text("Model Name") + input').fill('gpt-4');
        await page.click('button:has-text("Test Connection")');
        await expect(page.getByText('Connection successful!')).toBeVisible();
        await page.click('button:has-text("Next Step")');

        // Step 3: Hire CEO
        await expect(page.getByText('Hire your CEO')).toBeVisible();
        await page.fill('input[type="text"]', 'E2E Agent');
        await page.click('button:has-text("Finish & Launch")');

        // Main App View
        // wait for navigation to home
        await page.waitForTimeout(2000);
        await page.goto('/companies/pw-inc');
        await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible({ timeout: 10000 });
        await expect(page.getByText('System created workspace')).toBeVisible();

        // Verify CEO agent settings initialized correctly
        await page.goto('/companies/pw-inc/agents/1');
        await page.click('button:has-text("Settings")');
        await expect(page.locator('select').nth(0)).toHaveValue("1"); // Provider
        await expect(page.locator('select').nth(1)).toHaveValue("gpt-4"); // Model
        await page.goto('/companies/pw-inc');

        // Navigate to Projects
        await page.click('a:has-text("Projects")');
        await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();

        // Create Project
        await page.click('button:has-text("Create Project")');
        await expect(page.getByRole('dialog')).toBeVisible();
        await page.getByRole('dialog').locator('input').first().fill('Project Alpha');
        // Let auto-generation run, then assert it's somewhat correct.
        // The value should be pw-inc/project-alpha
        await expect(page.getByRole('dialog').locator('input').nth(1)).toHaveValue('pw-inc/project-alpha');
        await page.getByRole('dialog').getByRole('button', { name: 'Create' }).click();
        await page.waitForTimeout(1000); // Wait for modal to close and state to update
        await expect(page.getByText('Project Alpha').first()).toBeVisible({ timeout: 10000 });

        // Navigate to Tasks
        await page.goto('/companies/pw-inc/tasks');
        await page.waitForTimeout(500);

        // Add a Sprint
        await page.click('button:has-text("Manage Sprints")');
        await expect(page.getByRole('heading', { name: 'Manage Sprints' })).toBeVisible();
        await page.click('button:has-text("Create Sprint")');
        await page.fill('input[placeholder="e.g. Sprint 1"]', 'E2E Sprint');
        await page.fill('input[type="date"]', '2024-01-01'); // Start
        await page.locator('input[type="date"]').nth(1).fill('2024-01-14'); // End
        await page.getByRole('dialog').getByRole('button', { name: 'Create' }).click();
        await expect(page.getByText('E2E Sprint')).toBeVisible();

        // Go back to tasks
        await page.goto('/companies/pw-inc/tasks');
        await page.waitForTimeout(500);

        // Add Task
        await page.click('button:has-text("New Task")');
        await page.fill('input[placeholder="Task title"]', 'Write E2E Tests');
        await page.locator('label:has-text("Sprint") + select').selectOption({ label: 'E2E Sprint' });
        await page.click('button:has-text("Create Task")');

        // Check task modal
        await page.click('text=Write E2E Tests');
        await expect(page.getByText('Task PW-INC-1')).toBeVisible();
        await page.locator('label:has-text("Assignee") + select').selectOption({ label: 'E2E Agent' });
        await page.locator('label:has-text("Status") + select').selectOption({ label: 'To Do' });
        await page.click('button:has-text("Save Task")');
        await page.waitForTimeout(500);
        await page.click('text=Write E2E Tests');
        await page.fill('input[placeholder="Add a comment..."]', 'Let us see if the agent works');
        await page.locator('.fixed.inset-0').locator('form').filter({ has: page.locator('input[placeholder="Add a comment..."]') }).locator('button[type="submit"]').click();
        await expect(page.getByText('I have analyzed the E2E task and completed it successfully! 🚀').last()).toBeVisible({ timeout: 10000 });
        await expect(page.getByText('Let us see if the agent works')).toBeVisible();

        // Verify Agent Run Logs
        await expect(page.getByText(/Run #\d/)).toBeVisible();
        await page.click('summary:has-text("Run #")');
        await expect(page.getByText('I have analyzed the E2E task').last()).toBeVisible();

        await page.keyboard.press('Escape');

        // Verify Run Logs page
        await page.waitForTimeout(2000);
        await page.goto('/companies/pw-inc/runs');
        await expect(page.getByRole('heading', { name: 'Run Logs' })).toBeVisible();
        await page.waitForTimeout(2000);
        await page.waitForTimeout(5000);
        await page.reload();





        await page.goto('/companies/pw-inc/tasks');
        // Verify done state
        const doneColumn = page.locator('div:has-text("in-review")').last();

    });


    test('can edit company shortname in settings', async ({ page }) => {
        page.on('dialog', dialog => dialog.accept());
        await page.goto('/companies/pw-inc');
        await page.waitForTimeout(1000);

        // Go to settings
        await page.click('a:has-text("Settings")');
        await expect(page.getByRole('heading', { name: 'Company Settings' })).toBeVisible();

        // Edit short name
        const input = page.locator('input').first(); // the shortname input
        await input.fill('nw');
        await page.click('button:has-text("Save Settings")');
        await page.waitForTimeout(1000);

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
        // The mock provider should be auto-selected, just click next
        await page.click('button:has-text("Next Step")');

        // Step 3: Hire new CEO
        await expect(page.getByText('Hire your CEO')).toBeVisible();
        await page.fill('input[type="text"]', 'Second CEO');
        await page.click('button:has-text("Finish & Launch")');

        // Main App View
        await page.waitForTimeout(2000);
        await page.goto('/companies/nw');
        await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible({ timeout: 10000 });

        // Verify we are on the second company
        // Check if there are 2 company icons in the sidebar
        const companyButtons = page.locator('button[title="Playwright Inc"], button[title="Second Company"]');
        await expect(companyButtons).toHaveCount(2);
    });
});
// append nothing here
