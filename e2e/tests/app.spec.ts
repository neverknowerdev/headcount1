import { test, expect } from '@playwright/test';

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

        // The layout component fetches companies
        await page.route('**/api/companies', async route => {
            if (route.request().method() === 'GET') {
                await route.fulfill({
                    status: 200,
                    contentType: 'application/json',
                    body: JSON.stringify([])
                });
            } else {
                await route.continue();
            }
        });

        // Force local storage wipe
        await page.addInitScript(() => {
            window.localStorage.removeItem('company-storage');
        });

        await page.goto('/add-company');

        // Give React a moment to mount and run hooks
        await page.waitForTimeout(1000);



        // Step 1: Create Company (Now on /add-company)
        await expect(page.getByText('Create a Workspace')).toBeVisible({timeout: 10000});
        await page.fill('input[placeholder="Acme Corp"]', 'Playwright Inc');
        await page.fill('input[placeholder="acme"]', 'pw-inc');
        await page.click('button:has-text("Next Step")');

        // ... rest of the test ...
    });
});
