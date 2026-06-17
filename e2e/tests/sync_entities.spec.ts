/**
 * E2E tests for cross-worktree filesystem sync.
 *
 * Verifies four properties introduced by the entity sync feature:
 *  1. Write-through — creating/updating entities immediately persists files to disk.
 *  2. Comment write-through — creating a comment rewrites the task's comments.json.
 *  3. Import — after a DB wipe, POST /api/settings/sync restores all entities from disk
 *              with IDs preserved so FK chains remain intact (including comments).
 *  4. Delete propagation — deleting an LLM provider removes its disk file so it is
 *                          not restored on the next sync.
 *
 * Filesystem layout verified here:
 *   ~/.paperclip2/companies/{shortName}/settings.yml
 *   ~/.paperclip2/companies/{shortName}/sprints/{id}.json
 *   ~/.paperclip2/companies/{shortName}/tasks/{id}/task.json
 *   ~/.paperclip2/companies/{shortName}/tasks/{id}/comments.json
 *   ~/.paperclip2/data/llm-providers/{id}.json
 */

import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';

test.describe('Entity filesystem sync (export / import)', () => {
    const env = loadE2EEnv();
    const shortName = 'ent-sync-test';
    const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');
    const compDataDir = path.join(paperclipBase, 'data', shortName);
    const compSettingsDir = path.join(paperclipBase, 'companies', shortName);

    let companyId: number;
    let providerId: number;
    let sprintId: number;
    let taskId: number;
    let commentId: number;

    test.beforeAll(async ({ request }) => {
        // Remove any leftover files from a previously failed run
        for (const dir of [compDataDir, compSettingsDir]) {
            if (fs.existsSync(dir)) fs.rmSync(dir, { recursive: true, force: true });
        }

        await request.post('/api/e2e/wipe-db');

        // Company
        const compRes = await request.post('/api/companies', {
            data: { name: 'Entity Sync Test', short_name: shortName, color: '#123456' },
        });
        expect(compRes.ok()).toBeTruthy();
        companyId = (await compRes.json()).id;

        // LLM Provider (global, no company FK)
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

        // Sprint
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

        // Task (no project or agent — those FKs are optional)
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

        // Comment on the task
        const commentRes = await request.post('/api/comments', {
            data: {
                task_id: taskId,
                author_type: 'user',
                content: 'Test sync comment',
            },
        });
        expect(commentRes.ok()).toBeTruthy();
        commentId = (await commentRes.json()).id;

        // comments.json is written by a goroutine; give it a moment to flush
        await new Promise(r => setTimeout(r, 400));
    });

    // ────────────────────────────────────────────────────────────────────────
    // Test 1: write-through (create + update)
    // ────────────────────────────────────────────────────────────────────────

    test('writes entity files to filesystem on create and update', async ({ request }) => {
        // -- Company settings.yml --
        const settingsPath = path.join(compSettingsDir, 'settings.yml');
        expect(fs.existsSync(settingsPath), `expected settings.yml at ${settingsPath}`).toBe(true);
        const settingsYml = fs.readFileSync(settingsPath, 'utf8');
        expect(settingsYml).toContain(`company_id: ${companyId}`);
        expect(settingsYml).toContain(`short_name: ${shortName}`);

        // -- LLM provider JSON --
        const providerPath = path.join(paperclipBase, 'data', 'llm-providers', `${providerId}.json`);
        expect(fs.existsSync(providerPath), `expected provider file at ${providerPath}`).toBe(true);
        const providerFile = JSON.parse(fs.readFileSync(providerPath, 'utf8'));
        expect(providerFile.id).toBe(providerId);
        expect(providerFile.name).toBe('Sync Provider');
        expect(providerFile.provider_type).toBe('openai');

        // -- Sprint JSON --
        const sprintPath = path.join(compSettingsDir, 'sprints', `${sprintId}.json`);
        expect(fs.existsSync(sprintPath), `expected sprint file at ${sprintPath}`).toBe(true);
        const sprintFile = JSON.parse(fs.readFileSync(sprintPath, 'utf8'));
        expect(sprintFile.id).toBe(sprintId);
        expect(sprintFile.name).toBe('Sync Sprint');
        expect(sprintFile.company_id).toBe(companyId);

        // -- Task directory + task.json --
        const taskDir = path.join(compSettingsDir, 'tasks', String(taskId));
        const taskPath = path.join(taskDir, 'task.json');
        expect(fs.existsSync(taskPath), `expected task.json at ${taskPath}`).toBe(true);
        const taskFile = JSON.parse(fs.readFileSync(taskPath, 'utf8'));
        expect(taskFile.id).toBe(taskId);
        expect(taskFile.title).toBe('Sync Task');
        expect(taskFile.sprint_id).toBe(sprintId);
        expect(taskFile.company_id).toBe(companyId);

        // -- comments.json --
        const commentsPath = path.join(taskDir, 'comments.json');
        expect(fs.existsSync(commentsPath), `expected comments.json at ${commentsPath}`).toBe(true);
        const commentsFile: any[] = JSON.parse(fs.readFileSync(commentsPath, 'utf8'));
        expect(commentsFile).toHaveLength(1);
        expect(commentsFile[0].id).toBe(commentId);
        expect(commentsFile[0].task_id).toBe(taskId);
        expect(commentsFile[0].content).toBe('Test sync comment');

        // -- Update propagation: task update rewrites task.json --
        const updateRes = await request.put(`/api/tasks/${taskId}`, {
            data: { title: 'Sync Task Updated', status: 'todo' },
        });
        expect(updateRes.ok()).toBeTruthy();

        const updatedFile = JSON.parse(fs.readFileSync(taskPath, 'utf8'));
        expect(updatedFile.title).toBe('Sync Task Updated');
        expect(updatedFile.status).toBe('todo');

        // -- Comment append: adding a second comment rewrites comments.json --
        const secondCommentRes = await request.post('/api/comments', {
            data: { task_id: taskId, author_type: 'user', content: 'Second comment' },
        });
        expect(secondCommentRes.ok()).toBeTruthy();
        await new Promise(r => setTimeout(r, 400));

        const commentsAfterSecond: any[] = JSON.parse(fs.readFileSync(commentsPath, 'utf8'));
        expect(commentsAfterSecond).toHaveLength(2);
        expect(commentsAfterSecond[1].content).toBe('Second comment');
    });

    // ────────────────────────────────────────────────────────────────────────
    // Test 2: full import after DB wipe (including comments)
    // ────────────────────────────────────────────────────────────────────────

    test('restores all entities and comments from filesystem after DB wipe', async ({ request }) => {
        // Wipe the database — every table cleared, autoincrement resets
        const wipeRes = await request.post('/api/e2e/wipe-db');
        expect(wipeRes.ok()).toBeTruthy();

        // Sanity check: DB is now empty
        const afterWipe = await (await request.get('/api/companies')).json();
        expect(afterWipe).toHaveLength(0);

        // Trigger sync — reads filesystem and imports missing records
        const syncRes = await request.post('/api/settings/sync');
        expect(syncRes.ok()).toBeTruthy();

        // Company restored with preserved ID
        const companiesAfterSync = await (await request.get('/api/companies')).json();
        const restoredCompany = companiesAfterSync.find((c: any) => c.short_name === shortName);
        expect(restoredCompany, 'company should be restored after sync').toBeDefined();
        expect(restoredCompany.id).toBe(companyId);

        // LLM provider restored with preserved ID
        const providersAfterSync = await (await request.get('/api/providers')).json();
        const restoredProvider = providersAfterSync.find((p: any) => p.id === providerId);
        expect(restoredProvider, 'provider should be restored after sync').toBeDefined();
        expect(restoredProvider.name).toBe('Sync Provider');

        // Sprint restored with preserved ID and correct company FK
        const sprintsAfterSync = await (await request.get(
            `/api/sprints?company_id=${restoredCompany.id}`
        )).json();
        const restoredSprint = sprintsAfterSync.find((s: any) => s.id === sprintId);
        expect(restoredSprint, 'sprint should be restored after sync').toBeDefined();
        expect(restoredSprint.name).toBe('Sync Sprint');
        expect(restoredSprint.company_id).toBe(restoredCompany.id);

        // Task restored with preserved ID, correct sprint FK, and updated title
        const tasksAfterSync = await (await request.get(
            `/api/tasks?company_id=${restoredCompany.id}`
        )).json();
        const restoredTask = tasksAfterSync.find((t: any) => t.id === taskId);
        expect(restoredTask, 'task should be restored after sync').toBeDefined();
        expect(restoredTask.title).toBe('Sync Task Updated');
        expect(restoredTask.sprint_id).toBe(sprintId);
        expect(restoredTask.company_id).toBe(restoredCompany.id);

        // Comments restored with preserved IDs and content
        const commentsAfterSync = await (await request.get(
            `/api/comments?task_id=${restoredTask.id}`
        )).json();
        expect(commentsAfterSync).toHaveLength(2);
        const restoredComment = commentsAfterSync.find((c: any) => c.id === commentId);
        expect(restoredComment, 'first comment should be restored after sync').toBeDefined();
        expect(restoredComment.content).toBe('Test sync comment');
        const secondComment = commentsAfterSync.find((c: any) => c.content === 'Second comment');
        expect(secondComment, 'second comment should be restored after sync').toBeDefined();
    });

    // ────────────────────────────────────────────────────────────────────────
    // Test 3: delete propagation
    // The previous sync (test 2) put the provider back in DB. Deleting it
    // must also remove the file so it is not re-imported on the next sync.
    // ────────────────────────────────────────────────────────────────────────

    test('removes provider file when provider is deleted', async ({ request }) => {
        const providerPath = path.join(paperclipBase, 'data', 'llm-providers', `${providerId}.json`);
        expect(fs.existsSync(providerPath), 'provider file should exist before delete').toBe(true);

        const deleteRes = await request.delete(`/api/providers/${providerId}`);
        expect(deleteRes.ok()).toBeTruthy();

        expect(fs.existsSync(providerPath), 'provider file should be removed after delete').toBe(false);

        // Confirm: sync now does NOT restore the deleted provider
        const syncRes = await request.post('/api/settings/sync');
        expect(syncRes.ok()).toBeTruthy();
        const providersAfterSync = await (await request.get('/api/providers')).json();
        const ghost = providersAfterSync.find((p: any) => p.id === providerId);
        expect(ghost, 'deleted provider should not be re-imported').toBeUndefined();
    });

    // ────────────────────────────────────────────────────────────────────────

    test.afterAll(async ({ request }) => {
        await request.post('/api/e2e/wipe-db');
        for (const dir of [compDataDir, compSettingsDir]) {
            if (fs.existsSync(dir)) fs.rmSync(dir, { recursive: true, force: true });
        }
        // Provider file may already be gone (removed by test 3); ignore if so
        const providerPath = path.join(paperclipBase, 'data', 'llm-providers', `${providerId}.json`);
        if (fs.existsSync(providerPath)) fs.rmSync(providerPath);
    });
});
