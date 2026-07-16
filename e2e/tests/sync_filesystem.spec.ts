import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';
import { loadE2EEnv } from '../helpers/env';

test.describe.serial('Entity filesystem sync (export / import)', () => {
    const env = loadE2EEnv();
    const shortName = 'ent-sync-test';
    const headcount1Base = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');
    const compDataDir = path.join(headcount1Base, 'data', shortName);
    const compSettingsDir = path.join(headcount1Base, 'companies', shortName);

    let companyId: number;
    let providerId: number;
    let sprintId: number;
    let taskId: number;
    let commentId: number;

    test.beforeAll(async ({ request }) => {
        for (const dir of [compDataDir, compSettingsDir]) {
            if (fs.existsSync(dir)) fs.rmSync(dir, { recursive: true, force: true });
        }
        await request.post('/api/e2e/wipe-db');

        const compRes = await request.post('/api/companies', {
            data: { name: 'Entity Sync Test', short_name: shortName, color: '#123456' },
        });
        expect(compRes.ok()).toBeTruthy();
        companyId = (await compRes.json()).id;

        const provRes = await request.post('/api/providers', {
            data: {
                name: 'Sync Provider',
                base_url: 'http://localhost:9999',
                api_key: 'sync-test-key',
                provider_type: 'openai',
                default_model: 'gpt-4',
                supported_models: 'gpt-4',
            },
        });
        expect(provRes.ok()).toBeTruthy();
        providerId = (await provRes.json()).id;

        const sprintRes = await request.post('/api/sprints', {
            data: {
                company_id: companyId,
                name: 'Sync Sprint',
                goal: 'Validate cross-worktree sync',
                start_date: '2024-01-01T00:00:00Z',
                end_date: '2024-01-14T00:00:00Z',
            },
        });
        expect(sprintRes.ok()).toBeTruthy();
        sprintId = (await sprintRes.json()).id;

        const taskRes = await request.post('/api/tasks', {
            data: {
                company_id: companyId,
                sprint_id: sprintId,
                title: 'Sync Task',
                task_type: 'implement',
                description: 'Testing filesystem sync',
                priority: 'Normal',
            },
        });
        expect(taskRes.ok()).toBeTruthy();
        taskId = (await taskRes.json()).id;

        const commentRes = await request.post('/api/comments', {
            data: { task_id: taskId, author_type: 'user', content: 'Test sync comment' },
        });
        expect(commentRes.ok()).toBeTruthy();
        commentId = (await commentRes.json()).id;
    });

    test('writes entity files to filesystem on create and update', async ({ request }) => {
        const settingsPath = path.join(compSettingsDir, 'settings.yml');
        expect(fs.existsSync(settingsPath)).toBe(true);
        const settingsYml = fs.readFileSync(settingsPath, 'utf8');
        expect(settingsYml).toContain(`company_id: ${companyId}`);

        const providerPath = path.join(headcount1Base, 'data', 'llm-providers', `${providerId}.json`);
        expect(fs.existsSync(providerPath)).toBe(true);
        const providerFile = JSON.parse(fs.readFileSync(providerPath, 'utf8'));
        expect(providerFile.name).toBe('Sync Provider');

        const sprintPath = path.join(compSettingsDir, 'sprints', `${sprintId}.json`);
        expect(fs.existsSync(sprintPath)).toBe(true);
        const sprintFile = JSON.parse(fs.readFileSync(sprintPath, 'utf8'));
        expect(sprintFile.name).toBe('Sync Sprint');

        const taskDir = path.join(compSettingsDir, 'tasks', String(taskId));
        const taskPath = path.join(taskDir, 'task.json');
        expect(fs.existsSync(taskPath)).toBe(true);
        const taskFile = JSON.parse(fs.readFileSync(taskPath, 'utf8'));
        expect(taskFile.title).toBe('Sync Task');

        const commentsPath = path.join(taskDir, 'comments.json');
        expect(fs.existsSync(commentsPath)).toBe(true);
        const commentsFile: any[] = JSON.parse(fs.readFileSync(commentsPath, 'utf8'));
        expect(commentsFile).toHaveLength(1);
        expect(commentsFile[0].id).toBe(commentId);
        expect(commentsFile[0].content).toBe('Test sync comment');

        // Task update rewrites task.json
        const updateRes = await request.put(`/api/tasks/${taskId}`, {
            data: { title: 'Sync Task Updated', status: 'todo' },
        });
        expect(updateRes.ok()).toBeTruthy();
        const updatedFile = JSON.parse(fs.readFileSync(taskPath, 'utf8'));
        expect(updatedFile.title).toBe('Sync Task Updated');

        // Adding a second comment rewrites comments.json
        const secondCommentRes = await request.post('/api/comments', {
            data: { task_id: taskId, author_type: 'user', content: 'Second comment' },
        });
        expect(secondCommentRes.ok()).toBeTruthy();
        const commentsAfterSecond: any[] = JSON.parse(fs.readFileSync(commentsPath, 'utf8'));
        expect(commentsAfterSecond).toHaveLength(3); // original + status_change + second comment
        expect(commentsAfterSecond[2].content).toBe('Second comment');
    });

    test('restores all entities and comments from filesystem after DB wipe', async ({ request }) => {
        await request.post('/api/e2e/wipe-db');
        expect(await (await request.get('/api/companies')).json()).toHaveLength(0);

        await request.post('/api/settings/sync');

        const companies = await (await request.get('/api/companies')).json();
        const restoredCompany = companies.find((c: any) => c.short_name === shortName);
        expect(restoredCompany).toBeDefined();
        expect(restoredCompany.id).toBe(companyId);

        const providers = await (await request.get('/api/providers')).json();
        expect(providers.find((p: any) => p.id === providerId)).toBeDefined();

        const sprints = await (await request.get(`/api/sprints?company_id=${companyId}`)).json();
        expect(sprints.find((s: any) => s.id === sprintId)).toBeDefined();

        const tasks = await (await request.get(`/api/tasks?company_id=${companyId}`)).json();
        const restoredTask = tasks.find((t: any) => t.id === taskId);
        expect(restoredTask).toBeDefined();
        expect(restoredTask.title).toBe('Sync Task Updated');

        const comments = await (await request.get(`/api/comments?task_id=${taskId}`)).json();
        expect(comments).toHaveLength(3); // original + status_change + second comment
        expect(comments.find((c: any) => c.id === commentId)).toBeDefined();
    });

    test('removes provider file when provider is deleted', async ({ request }) => {
        const providerPath = path.join(headcount1Base, 'data', 'llm-providers', `${providerId}.json`);
        expect(fs.existsSync(providerPath)).toBe(true);

        await request.delete(`/api/providers/${providerId}`);
        expect(fs.existsSync(providerPath)).toBe(false);

        await request.post('/api/settings/sync');
        const providers = await (await request.get('/api/providers')).json();
        expect(providers.find((p: any) => p.id === providerId)).toBeUndefined();
    });

    test.afterAll(async ({ request }) => {
        await request.post('/api/e2e/wipe-db');
        for (const dir of [compDataDir, compSettingsDir]) {
            if (fs.existsSync(dir)) fs.rmSync(dir, { recursive: true, force: true });
        }
        const providerPath = path.join(headcount1Base, 'data', 'llm-providers', `${providerId}.json`);
        if (fs.existsSync(providerPath)) fs.rmSync(providerPath);
    });
});

test.describe('Filesystem as Source of Truth', () => {
    const env = loadE2EEnv();
    const companyShortName = 'fs-sync-test';
    const headcount1Base = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');
    const companyPath = path.join(headcount1Base, 'data', companyShortName);
    const backupPath = path.join(os.tmpdir(), `headcount1-backup-${Date.now()}`);

    test.beforeAll(async ({ request }) => {
        if (fs.existsSync(companyPath)) {
            fs.rmSync(companyPath, { recursive: true, force: true });
        }
        // Wipe DB: this test runs after agent_settings and app tests,
        // which leave companies behind that break the filesystem redirect logic.
        await request.post('/api/e2e/wipe-db');
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

        const projectPath = path.join(headcount1Base, 'data', 'artifacts', companyShortName, 'Test Project');
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
