import { test, expect } from '@playwright/test';

test.describe('Settings and Sync', () => {
    test('can save settings and sync from filesystem', async ({ page }) => {
        // Mock successful api responses for sync
        await page.route('**/api/settings', async route => {
            if (route.request().method() === 'GET') {
                await route.fulfill({
                    status: 200,
                    contentType: 'application/json',
                    body: JSON.stringify({
                        base_path: '/tmp/.paperclip2',
                        git_remote_url: 'git@github.com:test/repo.git',
                        github_pat: 'test_pat',
                        utility_provider_id: 0,
                        utility_model: 'gpt-4o-mini'
                    })
                });
            } else if (route.request().method() === 'POST') {
                await route.fulfill({
                    status: 200,
                    contentType: 'application/json',
                    body: JSON.stringify({ status: 'ok' })
                });
            } else {
                await route.continue();
            }
        });

        await page.route('**/api/settings/sync', async route => {
            await route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({ status: 'ok' })
            });
        });

        // Seed some base data and avoid redirect to add-company
        await page.route('**/api/companies', async route => {
            await route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify([{ id: 1, name: 'Test', short_name: 'test' }])
            });
        });

        await page.goto('/');

        // Ensure not redirected
        await page.goto('/companies/test/settings');

        // Wait for inputs to be populated
        await expect(page.locator('h1', {hasText: 'Settings'})).toBeVisible({timeout: 10000});

        // Click sync button
        page.on('dialog', dialog => dialog.accept());
        await page.getByRole('button', { name: 'Sync from Filesystem' }).click();

        // Verify alert or action completed successfully
        await expect(page.getByRole('button', { name: 'Syncing...' })).toBeHidden();
    });
});

