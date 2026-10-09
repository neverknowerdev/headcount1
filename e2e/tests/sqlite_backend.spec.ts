import { test, expect, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';
import { resetE2E } from '../helpers/reset';
import { call, finishWork, setScenario, startTask } from '../helpers/workflow';

/**
 * SQLite-backend edge cases.
 *
 * The full E2E suite runs against PostgreSQL (the cloud/primary backend); this
 * spec is the dedicated coverage for the local SQLite backend — the on-disk
 * layout, WAL journaling, and the autoincrement-reset behaviour that only apply
 * when there is no DATABASE_URL. It is skipped when a Postgres DATABASE_URL is
 * set (the server has no on-disk database then), so it is a no-op inside the
 * Postgres run and is invoked explicitly on the SQLite CI leg.
 */
const env = loadE2EEnv();
const isPostgres = (process.env.DATABASE_URL || '').startsWith('postgres://');

const base = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');
const dbFile = path.join(base, 'db', 'headcount1-e2e.db');
const repoRoot = path.join(process.cwd(), '..');

test.describe('SQLite backend storage', () => {
    test.skip(isPostgres, 'SQLite-only: no on-disk database when backed by Postgres');

    test('database lives at {basePath}/db/headcount1-e2e.db', async () => {
        expect(fs.existsSync(dbFile)).toBe(true);
    });

    test('database file has the SQLite format magic header', async () => {
        // Every SQLite database begins with the 16-byte header
        // "SQLite format 3\0" — 15 printable bytes followed by a NUL terminator.
        // A cheap way to assert it is a real SQLite file and not, say, an
        // accidentally-created empty/placeholder file. (The header's final byte
        // is checked separately so this source file stays NUL-free — a literal
        // NUL would make git/GitHub treat the whole spec as a binary blob.)
        const fd = fs.openSync(dbFile, 'r');
        try {
            const buf = Buffer.alloc(16);
            fs.readSync(fd, buf, 0, 16, 0);
            expect(buf.subarray(0, 15).toString('latin1')).toBe('SQLite format 3');
            expect(buf[15]).toBe(0);
        } finally {
            fs.closeSync(fd);
        }
    });

    test('WAL journaling is active: -wal and -shm sidecars exist', async () => {
        // journal_mode(WAL) creates a write-ahead log (-wal) and a shared-memory
        // index (-shm) next to the database while a connection is open. Their
        // presence is what lets multiple worktree processes see each other's
        // writes (see the cross-process test in shared_db.spec).
        expect(fs.existsSync(dbFile + '-wal')).toBe(true);
        expect(fs.existsSync(dbFile + '-shm')).toBe(true);
    });

    test('no stray database files are created in the repo root', async () => {
        // A CWD-relative DSN would drop the DB (and its WAL sidecars) into the
        // repo root instead of {basePath}/db. Guard against a regression that
        // reintroduces a working-directory database.
        for (const name of ['headcount1-e2e.db', 'orchestrator.db']) {
            for (const suffix of ['', '-wal', '-shm']) {
                const stray = path.join(repoRoot, name + suffix);
                expect(fs.existsSync(stray), `unexpected stray db file: ${stray}`).toBe(false);
            }
        }
    });
});

test.describe.serial('SQLite autoincrement reset', () => {
    test.skip(isPostgres, 'SQLite-only: exercises the sqlite_sequence reset path');

    test.beforeAll(async ({ request }) => {
        // A clean wipe resets sqlite_sequence, so the next inserts start at 1.
        await resetE2E(request);
    });

    test('ids restart at 1 after a wipe and increment sequentially', async ({ request }) => {
        const first = await request.post('/api/companies', {
            data: { name: 'Seq One', short_name: 'seq-one', color: '#111111' },
        });
        expect(first.ok()).toBeTruthy();
        const firstCompany = await first.json();
        expect(firstCompany.id).toBe(1);

        const second = await request.post('/api/companies', {
            data: { name: 'Seq Two', short_name: 'seq-two', color: '#222222' },
        });
        expect(second.ok()).toBeTruthy();
        const secondCompany = await second.json();
        expect(secondCompany.id).toBe(2);
    });
});

/**
 * Export/import round-trip on the SQLite backend.
 *
 * Seed a full company tree — company, project, sprint, agent, skill, an MCP
 * server + credentialed account, a task, and a real engine run (with log
 * entries) driven by the mock LLM provider — then back it up, wipe the
 * database, restore from the archive, and assert every entity comes back
 * byte-for-byte, including the run's log entries. This is the SQLite
 * counterpart to the Go-level TestPostgresRestoreRealignsSequences.
 */
test.describe.serial('SQLite export/import round-trip', () => {
    test.skip(isPostgres, 'SQLite-only: backup/restore fidelity on the on-disk backend');

    test.beforeAll(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test.afterAll(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test('backup, wipe, restore preserves the full company tree, its workflow record and run logs', async ({ request }) => {
        test.setTimeout(120_000);

        // ── Seed: provider → company → project/sprint/agent/skill/mcp → task ──
        const provider = await postJSON(request, '/api/providers', {
            name: 'e2e-mock', base_url: env.E2E_MOCK_PROVIDER_URL, api_key: 'test-key',
            provider_type: 'openai', default_model: 'e2e-smart-model',
            supported_models: 'e2e-smart-model,e2e-cheap-model',
        });
        const company = await postJSON(request, '/api/companies', {
            name: 'Backup Co', short_name: 'backup-co', color: '#0ea5e9', provider_id: provider.id, model: 'e2e-smart-model',
        });
        const cheapSetting = await request.put('/api/default-model-settings/cheap', {
            data: { provider_id: provider.id, model: 'e2e-cheap-model' },
        });
        expect(cheapSetting.ok(), await cheapSetting.text()).toBeTruthy();
        const project = await postJSON(request, '/api/projects', {
            company_id: company.id, name: 'Web App', description: 'the primary web application',
        });
        const sprint = await postJSON(request, '/api/sprints', {
            company_id: company.id, name: 'Sprint 1', goal: 'ship the greeting',
        });
        const agent = await postJSON(request, '/api/agents', {
            company_id: company.id, name: 'Runner', system_prompt: 'You are the Runner agent. We sell boats.',
        });
        const skill = await postJSON(request, '/api/skills', {
            company_id: company.id, name: 'greeting-skill', description: 'knows how to greet',
        });
        const mcp = await postJSON(request, '/api/mcp-servers', {
            name: 'e2e-notes', transport: 'http', url: 'https://mcp.example.com/notes',
            display_name: 'Notes', auth_type: 'bearer',
        });
        await postJSON(request, `/api/mcp-servers/${mcp.id}/accounts`, {
            name: 'Primary', auth_token: 'super-secret-token',
        });
        // The executed task carries no project (a project would pull in
        // repo/workspace setup the run doesn't need).
        const task = await postJSON(request, '/api/tasks', {
            company_id: company.id, sprint_id: sprint.id, agent_id: agent.id, task_type: 'research',
            title: 'Do the thing', description: 'a task to execute',
        });

        // ── Run it through the workflow: the smart steps are the mock's
        // autopilot; the executor uses a tool and records a decision, so there
        // is a real session log and a real decision to carry across. ─────────
        await setScenario([
            { match: { phase: 'executor' }, replies: [
                call('ls', { path: '.' }),
                finishWork('done', 'All done.', { decisions: [{ title: 'Looked before answering', detail: 'Listed the workspace first.', reason: 'To be sure.' }] }),
            ] },
        ]);
        await startTask(request, task.id);
        await waitForTaskStatus(request, task.id, 'in-review', 90_000);

        // ── Snapshot BEFORE the backup ───────────────────────────────────────
        const before = await snapshot(request, company.id, task.id);
        // Sanity: there really is a workflow record and a session log
        // (otherwise the round-trip check below would be vacuously true).
        expect(before.tree).toHaveLength(2);
        expect(before.runs).toHaveLength(1);
        expect(Array.isArray(before.runs[0].log_entries)).toBe(true);
        expect(before.runs[0].log_entries.length).toBeGreaterThan(0);
        expect(before.steps.length).toBeGreaterThan(4);
        expect(before.decisions.length).toBeGreaterThan(3);
        expect(before.usage.totals.calls).toBe(5);
        expect(before.mcp?.accounts?.[0]?.has_token).toBe(true);

        // ── Back up, then wipe ───────────────────────────────────────────────
        const backup = await postJSON(request, '/api/backup', {});
        expect(backup.archive_path).toBeTruthy();

        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
        const afterWipe = await (await request.get('/api/companies')).json();
        expect((afterWipe as any[]).some((c) => c.short_name === 'backup-co')).toBe(false);

        // ── Restore from the archive ─────────────────────────────────────────
        const restore = await request.post('/api/backup/restore', { data: { archive_path: backup.archive_path } });
        expect(restore.ok(), `restore failed: ${restore.status()} ${await restore.text()}`).toBeTruthy();

        // ── Snapshot AFTER and compare field-by-field ────────────────────────
        const after = await snapshot(request, company.id, task.id);

        expect(pick(after.company, COMPANY_KEYS)).toEqual(pick(before.company, COMPANY_KEYS));
        expect(after.projects.map((p) => pick(p, PROJECT_KEYS)))
            .toEqual(before.projects.map((p) => pick(p, PROJECT_KEYS)));
        expect(after.sprints.map((s) => pick(s, SPRINT_KEYS)))
            .toEqual(before.sprints.map((s) => pick(s, SPRINT_KEYS)));
        expect(after.agents.map((a) => pick(a, AGENT_KEYS)))
            .toEqual(before.agents.map((a) => pick(a, AGENT_KEYS)));
        expect(after.skills.map((s) => pick(s, SKILL_KEYS)))
            .toEqual(before.skills.map((s) => pick(s, SKILL_KEYS)));
        expect(after.tasks.map((t) => pick(t, TASK_KEYS)))
            .toEqual(before.tasks.map((t) => pick(t, TASK_KEYS)));

        // MCP server + its credentialed account (the token stays sealed, so the
        // has_token flag — not the secret — is what must survive).
        expect(pick(after.mcp, MCP_KEYS)).toEqual(pick(before.mcp, MCP_KEYS));
        expect(after.mcp.accounts.map((a) => pick(a, MCP_ACCOUNT_KEYS)))
            .toEqual(before.mcp.accounts.map((a) => pick(a, MCP_ACCOUNT_KEYS)));

        // The executor session and — crucially — its log entries come back intact.
        expect(after.runs.map((run: any) => pick(run, RUN_KEYS))).toEqual(before.runs.map((run: any) => pick(run, RUN_KEYS)));
        expect(after.runs[0].log_entries).toEqual(before.runs[0].log_entries);

        // So does the task's whole workflow record: the tree of subtasks with
        // where each stands, the journal with every smart prompt, the
        // decisions, and the usage ledger down to the last call.
        expect(after.tree.map((t: any) => pick(t, TREE_KEYS))).toEqual(before.tree.map((t: any) => pick(t, TREE_KEYS)));
        expect(after.steps).toEqual(before.steps);
        expect(after.firstSmartStep.prompt).toBeTruthy();
        expect(after.firstSmartStep).toEqual(before.firstSmartStep);
        expect(after.decisions).toEqual(before.decisions);
        expect(after.usage).toEqual(before.usage);
        expect(after.calls).toEqual(before.calls);
    });
});

const COMPANY_KEYS = ['id', 'name', 'short_name', 'description', 'color'];
const PROJECT_KEYS = ['id', 'company_id', 'name', 'description'];
const SPRINT_KEYS = ['id', 'company_id', 'name', 'goal'];
const AGENT_KEYS = ['id', 'company_id', 'name', 'role_key', 'system_prompt', 'builtin', 'enabled'];
const SKILL_KEYS = ['id', 'company_id', 'name', 'description'];
const TASK_KEYS = ['id', 'company_id', 'sprint_id', 'agent_id', 'title', 'description', 'status', 'task_type'];
const TREE_KEYS = [
    'id', 'parent_id', 'root_task_id', 'depth', 'title', 'status', 'task_type', 'mode', 'phase', 'waiting_on',
    'origin_step_id', 'origin_phase', 'workflow_phase', 'result_reason', 'result_summary', 'refined_description', 'acceptance_criteria',
];
const MCP_KEYS = ['id', 'name', 'transport', 'url', 'display_name', 'auth_type'];
const MCP_ACCOUNT_KEYS = ['id', 'mcp_server_id', 'name', 'has_token'];
const RUN_KEYS = ['id', 'task_id', 'agent_id', 'name', 'status', 'attempt', 'report', 'result_description'];

function pick(obj: any, keys: string[]): Record<string, unknown> {
    return Object.fromEntries(keys.map((k) => [k, obj?.[k]]));
}

async function postJSON(request: APIRequestContext, url: string, data: unknown): Promise<any> {
    const res = await request.post(url, { data });
    if (!res.ok()) {
        throw new Error(`POST ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}

/**
 * Collects the company tree via the public API into a comparable shape. `mcp`
 * is the single custom server we created (built-ins are filtered out by name).
 */
async function snapshot(request: APIRequestContext, companyId: number, taskId: number) {
    const get = async (url: string) => {
        const res = await request.get(url);
        if (!res.ok()) throw new Error(`GET ${url} failed (${res.status()}): ${await res.text()}`);
        return res.json();
    };
    const companies = await get('/api/companies');
    const mcpServers = await get(`/api/mcp-servers?company_id=${companyId}`);
    const tree = await get(`/api/tasks/${taskId}/tree`);
    const steps = await get(`/api/tasks/${taskId}/steps`);
    return {
        company: (companies as any[]).find((c) => c.id === companyId),
        projects: await get(`/api/projects?company_id=${companyId}`),
        sprints: await get(`/api/sprints?company_id=${companyId}`),
        agents: await get(`/api/agents?company_id=${companyId}`),
        skills: await get(`/api/skills?company_id=${companyId}`),
        tasks: await get(`/api/tasks?company_id=${companyId}`),
        mcp: (mcpServers as any[]).find((s) => s.name === 'e2e-notes'),
        tree,
        runs: await get(`/api/tasks/${tree[tree.length - 1].id}/runs`),
        steps,
        firstSmartStep: await get(`/api/tasks/${taskId}/steps/${steps.find((step: any) => step.kind === 'smart_call').id}`),
        decisions: await get(`/api/tasks/${taskId}/decisions?subtree=true`),
        usage: await get(`/api/tasks/${taskId}/usage`),
        calls: (await get(`/api/usage/calls?company_id=${companyId}`)).calls,
    };
}
