import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';

test.describe('Filesystem as Source of Truth', () => {
    const companyShortName = 'fs-sync-test';
    const homeDir = os.homedir();
    const paperclipBase = path.join(homeDir, '.paperclip2');
    const companyPath = path.join(paperclipBase, 'data', companyShortName);
    const backupPath = path.join(os.tmpdir(), `paperclip-backup-${Date.now()}`);

    test.beforeAll(async ({ request }) => {
        if (fs.existsSync(companyPath)) {
            fs.rmSync(companyPath, { recursive: true, force: true });
        }
        // Create company via API — folder gets created automatically
        const res = await request.post('/api/companies', {
            data: { name: 'Filesystem Sync Inc', short_name: companyShortName, color: '#4f46e5' }
        });
        expect(res.ok()).toBeTruthy();
    });

    test('should maintain filesystem as source of truth and sync back', async ({ page }) => {
        // 1. Verify data is on disk
        await page.waitForTimeout(500);
        expect(fs.existsSync(companyPath)).toBe(true);

        // 2. Navigate to company and create a Project
        await page.goto(`/companies/${companyShortName}`);
        await page.click('a:has-text("Projects")');
        await page.click('button:has-text("Create Project")');
        await page.getByRole('dialog').locator('input').first().fill('Test Project');
        await page.getByRole('dialog').getByRole('button', { name: 'Create' }).click();
        await page.waitForTimeout(1000);

        const projectPath = path.join(paperclipBase, 'data', 'artifacts', companyShortName, 'Test Project');
        expect(fs.existsSync(projectPath)).toBe(true);

        // 3. Move the company folder away
        console.log(`Moving ${companyPath} to ${backupPath}`);
        fs.renameSync(companyPath, backupPath);

        // 4. Reload UI -> Should see onboarding (ListCompanies filters by filesystem)
        await page.goto('/');
        await page.waitForURL(/\/add-company/, { timeout: 10000 });
        await expect(page.getByText('Create a Workspace')).toBeVisible({ timeout: 10000 });

        // 5. Move the folder back
        console.log(`Moving ${backupPath} back to ${companyPath}`);
        fs.renameSync(backupPath, companyPath);

        // 6. Reload UI -> Should see the company again
        await page.goto('/');
        await expect(page.getByText('Loading...')).toBeHidden({ timeout: 10000 });
        await expect(page).toHaveURL(/.*\/companies\/fs-sync-test/, { timeout: 15000 });
        await expect(page.getByText('FS').first()).toBeVisible({ timeout: 10000 });

        // 7. Test Sync from Filesystem button
        await page.goto(`/companies/${companyShortName}/settings`);
        await page.click('button:has-text("Sync from Filesystem")');
        await page.waitForTimeout(1000);

        // Verify project is still there
        await page.goto(`/companies/${companyShortName}/projects`);
        await expect(page.getByText('Test Project').first()).toBeVisible();
    });

    test.afterAll(async () => {
        if (fs.existsSync(companyPath)) {
            fs.rmSync(companyPath, { recursive: true, force: true });
        }
        if (fs.existsSync(backupPath)) {
            fs.rmSync(backupPath, { recursive: true, force: true });
        }
    });
});
