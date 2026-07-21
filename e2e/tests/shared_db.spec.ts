import { test, expect } from '@playwright/test';
import { spawn, ChildProcess } from 'child_process';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';

const env = loadE2EEnv();

// The suite runs against both backends (see .github/workflows/e2e.yml). The
// on-disk SQLite file assertions below only make sense for the SQLite backend;
// with Postgres the database is remote and there is no {basePath}/db file. The
// cross-process and upload tests, by contrast, are backend-agnostic and run on
// both.
const isPostgres = (process.env.DATABASE_URL || '').startsWith('postgres://');

/**
 * The database is the single source of truth, shared by every process
 * pointed at the same BasePath: SQLite lives at {basePath}/db/ with WAL so
 * multiple worktree processes see each other's writes. This spec boots a
 * SECOND server process on another port against the same E2E home and
 * verifies both directions of visibility — replacing the old filesystem
 * JSON mirror + sync mechanism.
 */
test.describe.serial('Shared database across processes', () => {
    // Unique per run: a stale second server from a previous run must never
    // be mistaken for ours.
    const secondPort = 18000 + (process.pid % 1000);
    let secondServer: ChildProcess | null = null;

    test.beforeAll(async ({ request }) => {
        await request.post('/api/e2e/wipe-db');
    });

    test.afterAll(async () => {
        // `go run` wraps the compiled binary in a child process; kill the
        // whole process group so the actual server dies too.
        if (secondServer?.pid) {
            try { process.kill(-secondServer.pid, 'SIGKILL'); } catch { /* already gone */ }
        }
    });

    test('SQLite lives under {basePath}/db with WAL enabled', async () => {
        test.skip(isPostgres, 'SQLite-only: with a Postgres backend there is no on-disk {basePath}/db file');
        const dbFile = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1', 'db', 'headcount1-e2e.db');
        expect(fs.existsSync(dbFile)).toBe(true);
        // WAL journaling leaves a -wal sidecar next to the database while
        // connections are open.
        expect(fs.existsSync(dbFile + '-wal')).toBe(true);
        // No CWD-relative database file in the repo root anymore.
        expect(fs.existsSync(path.join(process.cwd(), '..', 'headcount1-e2e.db'))).toBe(false);
        expect(fs.existsSync(path.join(process.cwd(), '..', 'orchestrator.db'))).toBe(false);
    });

    test('a second server process sees data written by the first, and vice versa', async ({ request }) => {
        test.setTimeout(180_000);

        // Entity created through the primary server (port 8080).
        const companyRes = await request.post('/api/companies', {
            data: { name: 'Shared DB Co', short_name: 'shared-db', color: '#123456' },
        });
        expect(companyRes.ok()).toBeTruthy();
        const company = await companyRes.json();

        // Boot a second server process against the SAME headcount1 home.
        const projectRoot = path.resolve(__dirname, '..', '..');
        secondServer = spawn('go', ['run', '.'], {
            cwd: projectRoot,
            detached: true, // own process group, so afterAll can kill go run's child too
            env: {
                ...process.env,
                E2E_MODE: 'true',
                E2E_HEADCOUNT1_HOME: env.E2E_HEADCOUNT1_HOME,
                PORT: String(secondPort),
            },
            stdio: 'ignore',
        });

        // Wait for it to come up.
        const secondBase = `http://localhost:${secondPort}`;
        await expect
            .poll(async () => {
                try {
                    const res = await fetch(`${secondBase}/api/ping`);
                    return res.ok;
                } catch {
                    return false;
                }
            }, { timeout: 120_000, intervals: [1000] })
            .toBe(true);

        // First direction: the second process reads the company created by
        // the first process.
        const listRes = await fetch(`${secondBase}/api/companies`);
        expect(listRes.ok).toBeTruthy();
        const companies = await listRes.json();
        expect((companies as any[]).some(c => c.short_name === 'shared-db')).toBe(true);

        // Second direction: create a sprint via the second process, read it
        // via the first.
        const sprintRes = await fetch(`${secondBase}/api/sprints`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ company_id: company.id, name: 'Cross-process sprint', goal: 'shared db' }),
        });
        expect(sprintRes.ok).toBeTruthy();

        const sprintsRes = await request.get(`/api/sprints?company_id=${company.id}`);
        expect(sprintsRes.ok()).toBeTruthy();
        const sprints = await sprintsRes.json();
        expect((sprints as any[]).some(s => s.name === 'Cross-process sprint')).toBe(true);
    });

    test('uploaded attachments land under {basePath}/uploads/{taskID}', async ({ request }) => {
        const companies = await (await request.get('/api/companies')).json();
        const company = (companies as any[]).find(c => c.short_name === 'shared-db');
        expect(company).toBeTruthy();

        // Tasks reference a sprint (FK-enforced); reuse the cross-process one.
        const sprints = await (await request.get(`/api/sprints?company_id=${company.id}`)).json();
        expect(sprints.length).toBeGreaterThan(0);
        const taskRes = await request.post('/api/tasks', {
            data: { company_id: company.id, title: 'Upload target', sprint_id: sprints[0].id },
        });
        expect(taskRes.ok()).toBeTruthy();
        const task = await taskRes.json();

        const uploadRes = await request.post('/api/attachments', {
            multipart: {
                task_id: String(task.id),
                file: {
                    name: 'note.txt',
                    mimeType: 'text/plain',
                    buffer: Buffer.from('attachment content'),
                },
            },
        });
        expect(uploadRes.ok()).toBeTruthy();
        const attachment = await uploadRes.json();

        const uploadsDir = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1', 'uploads', String(task.id));
        expect(fs.existsSync(uploadsDir)).toBe(true);
        expect(attachment.file_path.startsWith(uploadsDir)).toBe(true);
        expect(fs.readFileSync(attachment.file_path, 'utf8')).toBe('attachment content');
    });
});
