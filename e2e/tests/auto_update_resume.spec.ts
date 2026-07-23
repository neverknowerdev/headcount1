import { test, expect } from '@playwright/test';
import { spawn, spawnSync, ChildProcess } from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { startMockProviderServer } from '../fixtures/mock-provider-server';

/**
 * Graceful-drain + resume across a restart (the auto-update guarantee).
 *
 * When the server is asked to shut down for an update, an in-flight agent run
 * must not be lost: it should stop accepting the next turn, persist its
 * conversation mid-flight, and — after the binary restarts — pick that exact
 * conversation back up and run it to completion.
 *
 * This spec exercises the REAL lifecycle end-to-end against a dedicated,
 * fully-isolated server process (its own headcount1 home + SQLite DB + port)
 * so it can send it a real SIGTERM and restart it without touching the shared
 * suite server:
 *
 *   1. Kick a run whose first LLM turn is a report_status tool call.
 *   2. Hold that LLM response so the run is provably blocked mid-turn.
 *   3. SIGTERM the server; wait until its log proves draining has engaged.
 *   4. Release the held response → the run pauses (status "interrupted",
 *      conversation persisted, task still locked) and the process exits
 *      cleanly (drain did not hang).
 *   5. Restart the binary against the same home → it resumes the interrupted
 *      run, runs the report_status tool call that was pending at pause time,
 *      makes its next LLM call (finish_task), and completes the task.
 *
 * A `go run .` wrapper would swallow SIGTERM (it reaches the wrapper, not the
 * server), so this spawns the compiled binary directly.
 */
test.describe.serial('Auto-update: drain and resume in-flight runs', () => {
    const repoRoot = path.resolve(__dirname, '..', '..');
    const isPostgres = (process.env.DATABASE_URL || '').startsWith('postgres://');

    const port = 18500 + (process.pid % 500);
    const base = `http://localhost:${port}`;

    let home = '';
    let binPath = '';
    let mock: { baseUrl: string; port: number; stop: () => Promise<void> } | null = null;
    let server: ChildProcess | null = null;
    let serverLog = '';

    // Skip on the Postgres CI leg: this test deliberately runs an isolated
    // server on its own SQLite database so its interrupted run can't be raced
    // by the shared suite server. A shared Postgres backend would break that
    // isolation (both servers would resume the same run).
    test.skip(isPostgres, 'isolated-SQLite test: incompatible with a shared Postgres backend');

    /** Start the isolated server binary; resolves once /api/ping answers. */
    async function startServer(): Promise<void> {
        const child = spawn(binPath, [], {
            cwd: repoRoot,
            env: {
                ...process.env,
                DATABASE_URL: '', // force the isolated on-disk SQLite DB under `home`
                E2E_MODE: 'true',
                E2E_HEADCOUNT1_HOME: home,
                PORT: String(port),
            },
            stdio: ['ignore', 'pipe', 'pipe'],
        });
        child.stdout?.on('data', (d) => { serverLog += d.toString(); });
        child.stderr?.on('data', (d) => { serverLog += d.toString(); });
        server = child;

        await expect
            .poll(async () => {
                try {
                    return (await fetch(`${base}/api/ping`)).ok;
                } catch {
                    return false;
                }
            }, { timeout: 60_000, intervals: [500] })
            .toBe(true);
    }

    /** SIGTERM the server and resolve once the process has fully exited. */
    async function stopServer(): Promise<void> {
        const child = server;
        if (!child || child.exitCode !== null) return;
        const exited = new Promise<void>((resolve) => child.once('exit', () => resolve()));
        child.kill('SIGTERM');
        await exited;
    }

    async function postJSON(url: string, data: unknown): Promise<any> {
        const res = await fetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data),
        });
        if (!res.ok) {
            let detail = '';
            try { detail = await res.text(); } catch { /* ignore */ }
            throw new Error(`POST ${url} failed: ${res.status} ${detail}`);
        }
        return res.json();
    }

    test.beforeAll(async () => {
        if (isPostgres) return;
        home = fs.mkdtempSync(path.join(os.tmpdir(), 'hc1-resume-'));

        // Prefer the CI-prebuilt binary; otherwise build one now. Spawning the
        // binary directly (not `go run .`) is what lets SIGTERM reach it.
        const prebuilt = path.join(repoRoot, 'agent-orchestrator');
        if (fs.existsSync(prebuilt)) {
            binPath = prebuilt;
        } else {
            binPath = path.join(home, 'server-bin');
            const build = spawnSync('go', ['build', '-o', binPath, '.'], { cwd: repoRoot, encoding: 'utf8' });
            expect(build.status, `go build failed: ${build.stderr}`).toBe(0);
        }

        mock = await startMockProviderServer();
    });

    test.afterAll(async () => {
        await stopServer();
        if (mock) await mock.stop();
        // Best-effort: the server's background setup script may still be writing
        // a python venv under `home` as it's torn down, which can race rmSync.
        // A leaked temp dir under /tmp is harmless, so never fail teardown on it.
        if (home) {
            try { fs.rmSync(home, { recursive: true, force: true }); } catch { /* leak it */ }
        }
    });

    test('an interrupted run resumes and completes after restart', async () => {
        test.setTimeout(180_000);
        const mockUrl = mock!.baseUrl;
        const resumeMarker = 'progress recorded after resume';

        // ── Boot the isolated server and seed a runnable task ────────────────
        await startServer();

        const provider = await postJSON(`${base}/api/providers`, {
            name: 'mock', base_url: mockUrl, api_key: 'test-key',
            provider_type: 'openai', default_model: 'e2e-mock-model', supported_models: 'e2e-mock-model',
        });
        const company = await postJSON(`${base}/api/companies`, {
            name: 'Resume Co', short_name: 'rc', color: '#0ea5e9',
        });
        const sprint = await postJSON(`${base}/api/sprints`, {
            company_id: company.id, name: 'Sprint 1', goal: 'ship it',
        });
        const agent = await postJSON(`${base}/api/agents`, {
            company_id: company.id, name: 'Runner', system_prompt: 'You do the work.',
            model: 'e2e-mock-model', provider_id: provider.id,
        });
        const task = await postJSON(`${base}/api/tasks`, {
            company_id: company.id, sprint_id: sprint.id, agent_id: agent.id,
            title: 'Resumable task', description: 'a task to interrupt and resume', task_type: 'implement',
        });

        // Turn 1 reports progress (a tool call, so the run has more to do and is
        // a valid pause point); turn 2 finishes. finish_task is terminal.
        const scenario = {
            entries: [
                { tool_call: { id: 'rs-1', name: 'report_status', arguments: { status: resumeMarker } } },
                { tool_call: { id: 'ft-1', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'Completed after resume.' } } },
            ],
        };
        expect((await fetch(`${mockUrl}/__test/set-scenario`, {
            method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(scenario),
        })).ok).toBeTruthy();

        // Hold the LLM response so we can catch the run provably mid-turn.
        expect((await fetch(`${mockUrl}/__test/hold`, { method: 'POST' })).ok).toBeTruthy();

        // ── Kick the run; wait until it's blocked on its first LLM call ──────
        const kick = await fetch(`${base}/api/tasks/${task.id}`, {
            method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ status: 'to-do' }),
        });
        expect(kick.ok).toBeTruthy();

        await expect
            .poll(async () => {
                const r = await (await fetch(`${mockUrl}/__test/requests`)).json();
                return r.completionsReceived as number;
            }, { timeout: 30_000, intervals: [200], message: 'run should reach its first LLM call' })
            .toBeGreaterThanOrEqual(1);

        const runsBefore = await (await fetch(`${base}/api/tasks/${task.id}/runs`)).json();
        expect(runsBefore.length).toBe(1);
        const runId = runsBefore[0].id;
        expect(runsBefore[0].status).toBe('running');

        // ── Trigger the update shutdown; drain must engage before we release ─
        const drainMark = serverLog.length;
        const stopped = stopServer();
        await expect
            .poll(() => serverLog.slice(drainMark).includes('Draining active agent runs'),
                { timeout: 30_000, intervals: [100], message: 'server should begin draining on SIGTERM' })
            .toBe(true);

        // Draining is now in effect: releasing the held response lets the run
        // receive it and pause at the turn boundary instead of continuing.
        expect((await fetch(`${mockUrl}/__test/release`, { method: 'POST' })).ok).toBeTruthy();

        // The process must exit on its own — proof the drain completed (the run
        // paused) rather than hanging until the timeout.
        await stopped;
        expect(server?.exitCode).toBe(0);
        // Only the first (held) LLM call happened; the run paused before making
        // the finish_task call.
        const afterPause = await (await fetch(`${mockUrl}/__test/requests`)).json();
        expect(afterPause.completionsReceived).toBe(1);

        // ── Restart: the new process resumes the interrupted run ────────────
        const resumeMark = serverLog.length;
        await startServer();

        // Proof the resume path actually fired (not just that a fresh run ran).
        await expect
            .poll(() => /Resuming \d+ interrupted run/.test(serverLog.slice(resumeMark)),
                { timeout: 30_000, intervals: [200], message: 'restarted server should resume the interrupted run' })
            .toBe(true);

        // The resumed run completes and the task reaches its finish status.
        await expect
            .poll(async () => {
                const rs = await (await fetch(`${base}/api/tasks/${task.id}/runs`)).json();
                return rs.find((r: any) => r.id === runId)?.status ?? '';
            }, { timeout: 60_000, intervals: [500], message: 'resumed run should complete' })
            .toBe('completed');

        const finalTask = await (await fetch(`${base}/api/tasks/${task.id}`)).json();
        expect(finalTask.status).toBe('in-review');

        // The report_status tool call that was pending at pause time must have
        // run on resume — its side effect is the run's current_status.
        const finalRun = await (await fetch(`${base}/api/runs/${runId}`)).json();
        expect(finalRun.current_status).toBe(resumeMarker);

        // Exactly two LLM calls total across both processes: turn 1 (before
        // pause) and turn 2 (finish_task, after resume). The resumed
        // report_status executes locally, not via the LLM.
        const finalReqs = await (await fetch(`${mockUrl}/__test/requests`)).json();
        expect(finalReqs.completionsReceived).toBe(2);
    });
});
