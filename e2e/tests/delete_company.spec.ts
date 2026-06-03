import { test, expect } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';
import * as fs from 'fs';
import * as path from 'path';

const env = loadE2EEnv();

test.describe('Delete Company', () => {
    test('can delete a company with confirmation and archiving', async ({ page, request }) => {
        // Create a company via API
        const companyRes = await request.post('/api/companies', {
            data: {
                name: 'Delete Test Company',
                short_name: 'dtc',
                color: '#ff0000'
            }
        });
        expect(companyRes.ok()).toBeTruthy();
        const company = await companyRes.json();

        // Verify company exists in filesystem
        const companyPath = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2', 'data', 'dtc');
        expect(fs.existsSync(companyPath)).toBe(true);

        // Navigate to company settings
        await page.goto(`/companies/dtc/settings`);
        await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible({ timeout: 10000 });

        // Verify Danger Zone section exists
        await expect(page.getByText('Danger Zone')).toBeVisible();
        await expect(page.getByText('Delete this company')).toBeVisible();

        // Click delete button
        await page.getByRole('button', { name: 'Delete Company' }).click();

        // Verify confirmation dialog appears
        await expect(page.getByText('Warning: This action cannot be undone!')).toBeVisible();
        await expect(page.getByText('To confirm, type the company short name:')).toBeVisible();

        // Try to delete without typing confirmation - button should be disabled
        const deleteConfirmButton = page.getByRole('button', { name: 'Delete Company' }).last();
        await expect(deleteConfirmButton).toBeDisabled();

        // Type wrong confirmation - button should still be disabled
        await page.fill('input[placeholder="dtc"]', 'wrong');
        await expect(deleteConfirmButton).toBeDisabled();

        // Type correct confirmation
        await page.fill('input[placeholder="dtc"]', 'dtc');
        await expect(deleteConfirmButton).toBeEnabled();

        // Click cancel and verify dialog closes
        await page.getByRole('button', { name: 'Cancel' }).click();
        await expect(page.getByText('Warning: This action cannot be undone!')).toBeHidden();

        // Open dialog again and confirm deletion
        await page.getByRole('button', { name: 'Delete Company' }).click();
        await page.fill('input[placeholder="dtc"]', 'dtc');
        await deleteConfirmButton.click();

        // Wait for navigation (either to another company or to add-company)
        await page.waitForURL(/\/(companies\/[^/]+|add-company)/, { timeout: 10000 });

        // Verify company is deleted from API
        const listRes = await request.get('/api/companies');
        const companies = await listRes.json();
        const deletedCompany = companies.find((c: any) => c.short_name === 'dtc');
        expect(deletedCompany).toBeUndefined();

        // Verify company directory is deleted from filesystem
        expect(fs.existsSync(companyPath)).toBe(false);

        // Verify archive was created
        const archiveDir = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2', 'archive');
        expect(fs.existsSync(archiveDir)).toBe(true);

        const archives = fs.readdirSync(archiveDir);
        const dtcArchive = archives.find(a => a.startsWith('dtc_') && a.endsWith('.tar.gz'));
        expect(dtcArchive).toBeTruthy();

        // Verify archive is not empty
        const archivePath = path.join(archiveDir, dtcArchive!);
        const stats = fs.statSync(archivePath);
        expect(stats.size).toBeGreaterThan(0);
    });

    test('can delete company when multiple companies exist', async ({ page, request }) => {
        // Create two companies
        const company1Res = await request.post('/api/companies', {
            data: {
                name: 'First Company',
                short_name: 'fc',
                color: '#00ff00'
            }
        });
        expect(company1Res.ok()).toBeTruthy();

        const company2Res = await request.post('/api/companies', {
            data: {
                name: 'Second Company',
                short_name: 'sc',
                color: '#0000ff'
            }
        });
        expect(company2Res.ok()).toBeTruthy();

        // Navigate to second company settings
        await page.goto(`/companies/sc/settings`);
        await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible({ timeout: 10000 });

        // Delete second company
        await page.getByRole('button', { name: 'Delete Company' }).click();
        await page.fill('input[placeholder="sc"]', 'sc');
        await page.getByRole('button', { name: 'Delete Company' }).last().click();

        // Should redirect to another company (not necessarily fc)
        await page.waitForURL(/\/companies\/[^/]+/, { timeout: 10000 });

        // Verify second company is deleted
        const listRes = await request.get('/api/companies');
        const companies = await listRes.json();
        const deletedCompany = companies.find((c: any) => c.short_name === 'sc');
        expect(deletedCompany).toBeUndefined();
    });
});
