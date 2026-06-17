import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';

test.describe.serial('Backup & Restore', () => {
    test('can create backup via API', async ({ request }) => {
        const res = await request.post('/api/backup');
        expect(res.ok()).toBeTruthy();
        const body = await res.json();
        expect(body.archive_path).toBeTruthy();
        expect(body.message).toBe('Backup created successfully');
    });

    test('can get backup status', async ({ request }) => {
        const res = await request.get('/api/backup/status');
        expect(res.ok()).toBeTruthy();
        const body = await res.json();
        expect(body.backups).toBeDefined();
        expect(Array.isArray(body.backups)).toBeTruthy();
        expect(body.backups.length).toBeGreaterThan(0);
    });

    test('can list backups', async ({ request }) => {
        const res = await request.get('/api/backup/list');
        expect(res.ok()).toBeTruthy();
        const body = await res.json();
        expect(body.backups).toBeDefined();
        expect(Array.isArray(body.backups)).toBeTruthy();
    });

    test('backup and restore roundtrip preserves data', async ({ request }) => {
        // Create test data
        const companyRes = await request.post('/api/companies', {
            data: { name: 'Backup Test Company', short_name: 'bt' },
        });
        expect(companyRes.ok()).toBeTruthy();
        const company = await companyRes.json();

        // Create a project
        const projectRes = await request.post('/api/projects', {
            data: {
                company_id: company.id,
                name: 'Test Project',
                description: 'A test project for backup',
            },
        });
        expect(projectRes.ok()).toBeTruthy();
        const project = await projectRes.json();

        // Create a sprint
        const sprintRes = await request.post('/api/sprints', {
            data: {
                company_id: company.id,
                name: 'Test Sprint',
                goal: 'Test backup functionality',
                start_date: '2024-01-01T00:00:00Z',
                end_date: '2024-01-14T00:00:00Z',
            },
        });
        expect(sprintRes.ok()).toBeTruthy();
        const sprint = await sprintRes.json();

        // Create a task (status defaults to "backlog" in CreateTask handler)
        const taskRes = await request.post('/api/tasks', {
            data: {
                company_id: company.id,
                project_id: project.id,
                sprint_id: sprint.id,
                title: 'Test Task',
                description: 'Task for backup testing',
                priority: 'High',
            },
        });
        expect(taskRes.ok()).toBeTruthy();
        const task = await taskRes.json();

        // Create a comment
        const commentRes = await request.post('/api/comments', {
            data: {
                task_id: task.id,
                author_type: 'user',
                content: 'Test comment for backup',
            },
        });
        expect(commentRes.ok()).toBeTruthy();

        // Create backup
        const backupRes = await request.post('/api/backup');
        expect(backupRes.ok()).toBeTruthy();
        const backupBody = await backupRes.json();
        const archivePath = backupBody.archive_path;

        // Verify backup file exists
        expect(fs.existsSync(archivePath)).toBeTruthy();

        // Wipe the database
        const wipeRes = await request.post('/api/e2e/wipe-db');
        expect(wipeRes.ok()).toBeTruthy();

        // Verify data is gone
        const companiesAfterWipe = await request.get('/api/companies');
        const companiesList = await companiesAfterWipe.json();
        expect(companiesList.length).toBe(0);

        // Restore from backup
        const restoreRes = await request.post('/api/backup/restore', {
            data: { archive_path: archivePath },
        });
        expect(restoreRes.ok()).toBeTruthy();
        const restoreBody = await restoreRes.json();
        expect(restoreBody.message).toBe('Backup restored successfully');

        // Verify restored data
        const companiesAfterRestore = await request.get('/api/companies');
        const restoredCompanies = await companiesAfterRestore.json();
        expect(restoredCompanies.length).toBeGreaterThanOrEqual(1);

        const restoredCompany = restoredCompanies.find((c: any) => c.short_name === 'bt');
        expect(restoredCompany).toBeTruthy();
        expect(restoredCompany.name).toBe('Backup Test Company');

        // Verify projects
        const projectsRes = await request.get(`/api/projects?company_id=${restoredCompany.id}`);
        const projects = await projectsRes.json();
        expect(projects.length).toBeGreaterThanOrEqual(1);
        expect(projects[0].name).toBe('Test Project');

        // Verify sprints
        const sprintsRes = await request.get(`/api/sprints?company_id=${restoredCompany.id}`);
        const sprints = await sprintsRes.json();
        expect(sprints.length).toBeGreaterThanOrEqual(1);
        expect(sprints[0].name).toBe('Test Sprint');

        // Verify tasks
        const tasksRes = await request.get(`/api/tasks?company_id=${restoredCompany.id}`);
        const tasks = await tasksRes.json();
        expect(tasks.length).toBeGreaterThanOrEqual(1);
        expect(tasks[0].title).toBe('Test Task');
        expect(tasks[0].description).toBe('Task for backup testing');
        expect(tasks[0].status).toBe('backlog');
        expect(tasks[0].priority).toBe('High');

        // Verify comments
        const commentsRes = await request.get(`/api/comments?task_id=${tasks[0].id}`);
        const comments = await commentsRes.json();
        expect(comments.length).toBeGreaterThanOrEqual(1);
        expect(comments[0].content).toBe('Test comment for backup');
    });

    test('backup via Settings UI button', async ({ page }) => {
        // Navigate to settings for 'bt' company (exists after restore from roundtrip test)
        await page.goto('/companies/bt/settings');
        await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible({ timeout: 10000 });

        // Click Backup & Restore button and wait for navigation
        await Promise.all([
            page.waitForURL(/\/backup$/),
            page.click('button:has-text("Backup & Restore")'),
        ]);
        await expect(page.getByRole('heading', { name: 'Backup & Restore' })).toBeVisible();

        // Handle the alert dialog that appears on backup success
        page.on('dialog', async dialog => {
            await dialog.accept();
        });

        // Click Backup Now button
        await page.click('button:has-text("Backup Now")');

        // Wait for the backup to complete and the status to refresh.
        // Note: getByRole('list') is avoided here — Tailwind Preflight resets list-style
        // on <ul>, which Chrome uses to infer the implicit ARIA list role, so the locator
        // fails in CI even when the element is visible.
        await expect(page.getByText(/backup_.*\.tar\.gz/).first()).toBeVisible({ timeout: 30000 });
    });

    test.afterAll(async () => {
        // Remove bt's filesystem data so its comment IDs (written before the roundtrip
        // wipe and preserved in companies/bt/) don't collide with ent-sync-test IDs
        // when sync_filesystem.spec.ts calls POST /api/settings/sync.
        const env = loadE2EEnv();
        const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');
        for (const subDir of ['data/bt', 'companies/bt']) {
            const fullPath = path.join(paperclipBase, subDir);
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }
    });
});
