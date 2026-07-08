import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { execFileSync } from 'child_process';
import { loadE2EEnv } from '../helpers/env';

const env = loadE2EEnv();

async function dumpHindsight(): Promise<any> {
    const res = await fetch(`${env.E2E_HINDSIGHT_URL}/__admin/dump`);
    expect(res.ok).toBeTruthy();
    return res.json();
}

async function dumpHindsightBanks(): Promise<Record<string, any[]>> {
    return (await dumpHindsight()).banks as Record<string, any[]>;
}

/** Extracts a backup_*.tar.gz archive into a fresh temp dir and returns its path. */
function extractArchive(archivePath: string): string {
    const dest = fs.mkdtempSync(path.join(os.tmpdir(), 'paperclip-backup-'));
    execFileSync('tar', ['-xzf', archivePath, '-C', dest]);
    return dest;
}

test.describe.serial('Backup & Restore', () => {
    test.beforeAll(async ({ request }) => {
        // Start from a clean slate. wipe-db also resets the memory layer's
        // server-side state (hindsight_documents + the in-process "already
        // ensured" guards via Service.ResetEnsured), so the company created
        // below gets its bank configured from scratch even when its ID was
        // used by an earlier spec file — or by a failed previous attempt of
        // this serial group (retries re-run this hook).
        const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');
        for (const subDir of ['data/bt', 'companies/bt', 'data/artifacts/bt', 'workspace/bt']) {
            const fullPath = path.join(paperclipBase, subDir);
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }
        await request.post('/api/e2e/wipe-db');
        const resetRes = await fetch(`${env.E2E_HINDSIGHT_URL}/__admin/reset`, { method: 'POST' });
        expect(resetRes.ok).toBeTruthy();
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

        // Create a second project WITH a git repo so its .md docs get ingested
        // into the memory layer — the backup must carry the memory banks too.
        // (Kept separate from 'Test Project': cloning wipes project.json from
        // the artifacts dir, so repo projects don't round-trip as DB rows; the
        // memory banks themselves must round-trip regardless.)
        const memProjectRes = await request.post('/api/projects', {
            data: {
                company_id: company.id,
                name: 'Mem Project',
                description: 'Repo project seeding memory for backup',
                workspace_folder: 'bt/mem-project',
                repository_url: env.E2E_TEST_REPO_URL,
            },
        });
        expect(memProjectRes.ok()).toBeTruthy();
        const memProject = await memProjectRes.json();
        const bank = `company-${company.id}`;

        // Wait for the background doc ingestion to populate the company bank
        // (scoped to doc memories so stray non-doc memories can never satisfy
        // or break this wait).
        await expect.poll(async () => {
            const banks = await dumpHindsightBanks();
            return (banks[bank] || [])
                .map((m: any) => m.document_id)
                .filter((id: string) => (id || '').startsWith('doc:'))
                .sort();
        }, { timeout: 120_000, intervals: [2000], message: 'project docs should be ingested before backup' })
            .toEqual([
                `doc:${memProject.id}/README.md`,
                `doc:${memProject.id}/docs/gm-coin.md`,
                `doc:${memProject.id}/docs/icp-backend.md`,
            ]);
        const memTextsBefore = (await dumpHindsightBanks())[bank]
            .map((m: any) => m.text).sort();

        // EnsureBank ran during doc sync above: capture config/directives/
        // mental-models pre-backup so we can assert lossless round-trip
        // after restore (bank template export/import, separate from the
        // document-transfer archive).
        const dumpBefore = await dumpHindsight();
        const configBefore = dumpBefore.configs[bank];
        const directivesBefore = (dumpBefore.directives[bank] as any[]).map((d: any) => d.name).sort();
        const modelsBefore = ((dumpBefore.mental_models[bank] as any[]) || []).map((m: any) => m.id).sort();
        expect(configBefore.reflect_mission).toContain('Backup Test Company');

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

        // The bank's config/mental-models/directives manifest ("<bank>.template.json")
        // must ride along inside the archive next to the document-transfer
        // "<bank>.zip" — the two exports are complementary (see
        // pkg/hindsight/transfer.go ExportAllToDir).
        const extractedDir = extractArchive(archivePath);
        const hindsightDir = path.join(extractedDir, 'data', 'hindsight');
        expect(fs.existsSync(path.join(hindsightDir, `${bank}.zip`)), `expected ${bank}.zip in backup`).toBeTruthy();
        expect(fs.existsSync(path.join(hindsightDir, `${bank}.template.json`)), `expected ${bank}.template.json in backup`).toBeTruthy();
        fs.rmSync(extractedDir, { recursive: true, force: true });

        // Wipe the database
        const wipeRes = await request.post('/api/e2e/wipe-db');
        expect(wipeRes.ok()).toBeTruthy();

        // Wipe the memory backend too — the memory is truly lost and must
        // come back only through the backup archive.
        const resetRes = await fetch(`${env.E2E_HINDSIGHT_URL}/__admin/reset`, { method: 'POST' });
        expect(resetRes.ok).toBeTruthy();
        expect(Object.keys(await dumpHindsightBanks()).length).toBe(0);

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
        expect((projects as any[]).some((p: any) => p.name === 'Test Project')).toBeTruthy();

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

        // ── Memory: the bank was repopulated from the archive without loss ──
        await expect.poll(async () => {
            const banks = await dumpHindsightBanks();
            return (banks[bank] || [])
                .filter((m: any) => (m.document_id || '').startsWith('doc:')).length;
        }, { timeout: 60_000, message: 'memory bank should be re-imported on restore' }).toBe(3);
        const memTextsAfter = (await dumpHindsightBanks())[bank]
            .map((m: any) => m.text).sort();
        expect(memTextsAfter).toEqual(memTextsBefore);

        // And it is served through the /api/memory proxy again.
        const memList = await request.get(`/api/memory/banks/${bank}/memories?limit=50`);
        expect(memList.ok()).toBeTruthy();
        const memBody = await memList.json();
        expect(memBody.total).toBe(3);
        expect((memBody.items as any[]).some((i: any) => i.text.includes('GM Coin'))).toBeTruthy();

        // ── Bank config + mental models survive export/import losslessly ──
        // (the "<bank>.template.json" manifest applied by ImportAllFromDir,
        // separate from the document-transfer archive checked above).
        const configRes = await request.get(`/api/memory/banks/${bank}/config`);
        expect(configRes.ok()).toBeTruthy();
        const configAfter = (await configRes.json()).config;
        expect(configAfter).toEqual(configBefore);

        const directivesRes = await request.get(`/api/memory/banks/${bank}/directives`);
        expect(directivesRes.ok()).toBeTruthy();
        const directivesAfter = ((await directivesRes.json()).items as any[]).map((d: any) => d.name).sort();
        expect(directivesAfter).toEqual(directivesBefore);

        const modelsRes = await request.get(`/api/memory/banks/${bank}/mental-models`);
        expect(modelsRes.ok()).toBeTruthy();
        const modelsAfter = ((await modelsRes.json()).items as any[]).map((m: any) => m.id).sort();
        expect(modelsAfter).toEqual(modelsBefore);
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

        // Wait for the page to finish loading (the "Backup Now" button only renders after the
        // async status fetch completes, so waiting for it ensures the backup list is populated).
        await expect(page.getByRole('button', { name: 'Backup Now' })).toBeVisible({ timeout: 10000 });

        // Count how many backups exist before clicking so we can wait for a NEW one.
        const countBefore = await page.locator('li', { hasText: /backup_.*\.tar\.gz/ }).count();

        // Click Backup Now and accept the success dialog in parallel.
        const [dialog] = await Promise.all([
            page.waitForEvent('dialog'),
            page.click('button:has-text("Backup Now")'),
        ]);
        await dialog.accept();

        // Wait for the backup list to grow (a new entry appears after the dialog is dismissed).
        await expect(page.locator('li', { hasText: /backup_.*\.tar\.gz/ })).toHaveCount(countBefore + 1, { timeout: 30000 });
    });

    test.afterAll(async () => {
        // Remove bt's filesystem data so its comment IDs (written before the roundtrip
        // wipe and preserved in companies/bt/) don't collide with ent-sync-test IDs
        // when sync_filesystem.spec.ts calls POST /api/settings/sync.
        const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');
        for (const subDir of ['data/bt', 'companies/bt', 'data/artifacts/bt']) {
            const fullPath = path.join(paperclipBase, subDir);
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }
        // Leave the mock memory backend clean for the specs that follow.
        await fetch(`${env.E2E_HINDSIGHT_URL}/__admin/reset`, { method: 'POST' });
    });
});
