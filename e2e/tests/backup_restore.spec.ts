import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { resetE2E } from '../helpers/reset';

test.describe.serial('Backup & Restore', () => {
    // Tenant-scoped, per-user export/import (the real user-facing feature): a
    // team owner exports their subtree and re-imports it. Re-importing into the
    // same account dedups by domain key (short_name, provider slug, …) instead
    // of duplicating, and secrets stay decryptable throughout.
    test('per-user export + import round-trip dedups and keeps secrets decryptable', async ({ request }) => {
        // A company + a task + a provider with a known key.
        const companyRes = await request.post('/api/companies', {
            data: { name: 'Export Test Co', short_name: 'dx' },
        });
        expect(companyRes.ok(), await companyRes.text()).toBeTruthy();
        const company = await companyRes.json();

        const sprintRes = await request.post('/api/sprints', {
            data: { company_id: company.id, name: 'DX Sprint' },
        });
        expect(sprintRes.ok()).toBeTruthy();
        const sprint = await sprintRes.json();

        const taskRes = await request.post('/api/tasks', {
            data: { company_id: company.id, sprint_id: sprint.id, title: 'DX Task', priority: 'Normal' },
        });
        expect(taskRes.ok()).toBeTruthy();

        const secret = 'sk-export-' + Date.now();
        const provRes = await request.post('/api/providers', {
            data: {
                name: 'DX Provider',
                base_url: 'https://dx.example.test/v1',
                api_key: secret,
                provider_type: 'openai',
                default_model: 'dx-model',
            },
        });
        expect(provRes.ok(), await provRes.text()).toBeTruthy();
        const provider = await provRes.json();

        // Sanity: the key decrypts before export.
        const revealBefore = await request.get(`/api/e2e/reveal-provider/${provider.id}`);
        expect(revealBefore.ok()).toBeTruthy();
        expect((await revealBefore.json()).api_key).toBe(secret);

        // Export the current user's subtree (streamed tar.gz).
        const exportRes = await request.get('/api/data/export');
        expect(exportRes.ok(), await exportRes.text()).toBeTruthy();
        const archive = await exportRes.body();
        expect(archive.length).toBeGreaterThan(0);

        // Import it back into the same account. Everything in the archive already
        // exists here (we just exported our own live data), so dedup should reuse
        // it all and create nothing new.
        const importRes = await request.post('/api/data/import', {
            multipart: {
                file: { name: 'export.tar.gz', mimeType: 'application/gzip', buffer: archive },
            },
        });
        expect(importRes.ok(), await importRes.text()).toBeTruthy();
        const importBody = await importRes.json();
        expect(importBody.result.companies_restored).toBe(0);
        expect(importBody.result.tasks_restored).toBe(0);
        expect(importBody.result.providers_restored).toBe(0);

        // Still exactly ONE 'dx' company — not duplicated.
        const companiesRes = await request.get('/api/companies');
        const companies = await companiesRes.json();
        const dxCompanies = companies.filter((c: any) => c.short_name === 'dx' || c.short_name.startsWith('dx-'));
        expect(dxCompanies.length).toBe(1);

        // Still exactly ONE dx provider, and it still decrypts to the original
        // secret (ciphertext preserved through the export/import round-trip).
        const provsRes = await request.get('/api/providers');
        const provs = await provsRes.json();
        const dxProvs = provs.filter((p: any) => p.base_url === 'https://dx.example.test/v1');
        expect(dxProvs.length).toBe(1);
        expect(dxProvs[0].id).toBe(provider.id);

        const revealAfter = await request.get(`/api/e2e/reveal-provider/${dxProvs[0].id}`);
        expect(revealAfter.ok(), await revealAfter.text()).toBeTruthy();
        expect((await revealAfter.json()).api_key).toBe(secret);
    });

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
        await resetE2E(request);

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

    test('encrypted provider secret survives backup + restore and is decryptable', async ({ request }) => {
        // A provider API key is sealed at rest as enc:u1:<userID>:… under the
        // fixture user's data key (in E2E the DEK is deterministic — the stand-in
        // for a passkey PRF unlock). This proves the ciphertext round-trips
        // through backup + restore and is still decryptable afterwards.
        const secret = 'sk-roundtrip-' + Date.now();
        const createRes = await request.post('/api/providers', {
            data: {
                name: 'Secret Roundtrip Provider',
                base_url: 'https://example.test/v1',
                api_key: secret,
                provider_type: 'openai',
                default_model: 'e2e-model',
            },
        });
        expect(createRes.ok(), await createRes.text()).toBeTruthy();
        const provider = await createRes.json();
        // The API never echoes the raw key back — only a boolean flag.
        expect(provider.api_key).not.toBe(secret);
        expect(provider.has_api_key).toBe(true);

        // Sanity: while unlocked, the E2E reveal endpoint decrypts it correctly.
        const revealBefore = await request.get(`/api/e2e/reveal-provider/${provider.id}`);
        expect(revealBefore.ok()).toBeTruthy();
        expect((await revealBefore.json()).api_key).toBe(secret);

        // Back up, wipe, restore.
        const backupRes = await request.post('/api/backup');
        expect(backupRes.ok()).toBeTruthy();
        const archivePath = (await backupRes.json()).archive_path;

        await resetE2E(request);

        const restoreRes = await request.post('/api/backup/restore', {
            data: { archive_path: archivePath },
        });
        expect(restoreRes.ok(), await restoreRes.text()).toBeTruthy();

        // After restore the fixture user is re-unlocked with the same
        // deterministic DEK; the enc:u1 ciphertext (carried verbatim in the
        // backup) must decrypt back to the exact original secret.
        const revealAfter = await request.get(`/api/e2e/reveal-provider/${provider.id}`);
        expect(revealAfter.ok(), await revealAfter.text()).toBeTruthy();
        expect((await revealAfter.json()).api_key).toBe(secret);
    });

    test('Export & Import page reachable from Settings UI', async ({ page }) => {
        // Navigate to settings for 'bt' company (exists after restore from roundtrip test)
        await page.goto('/companies/bt/settings');
        await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible({ timeout: 10000 });

        // Click Export & Import and wait for navigation to the page.
        await Promise.all([
            page.waitForURL(/\/backup$/),
            page.click('button:has-text("Export & Import")'),
        ]);
        await expect(page.getByRole('heading', { name: 'Export & Import' })).toBeVisible();

        // The per-user export control is present.
        await expect(page.getByRole('button', { name: 'Export my data' })).toBeVisible({ timeout: 10000 });
    });

    test.afterAll(async () => {
        // Remove the specs' filesystem footprint so later specs start clean.
        const env = loadE2EEnv();
        const headcount1Base = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');
        for (const short of ['bt', 'dx']) {
            for (const root of ['repos', 'workspace', 'artifacts', 'logs', 'skills', 'uploads']) {
                const fullPath = path.join(headcount1Base, root, short);
                if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
            }
        }
    });
});
