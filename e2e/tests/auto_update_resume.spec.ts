import { test, expect } from '@playwright/test';
import { spawn, spawnSync, ChildProcess } from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { startMockProviderServer } from '../fixtures/mock-provider-server';
import { terminateProcess } from '../helpers/process';
import { fetchWithTimeout } from '../helpers/http';
import { resetE2EBase } from '../helpers/reset';

/**
 * Graceful-drain + resume across a restart (the deploy-restart guarantee).
 *
 * When the server is asked to shut down for a deploy (SIGTERM), an in-flight
 * agent run must not be lost: it should stop accepting the next turn, persist
 * its conversation mid-flight, and — after the binary restarts — pick that
 * exact conversation back up and run it to completion. This is the same
 * graceful restart a self-replacing deploy triggers (see deploy_webhook.spec).
 *
 * This spec exercises the REAL lifecycle end-to-end against a dedicated,
 * fully-isolated server process (its own headcount1 home + SQLite DB + port)
 * so it can send it a real SIGTERM and restart it without touching the shared
 * suite server:
 *
 *   1. Kick a run whose first LLM turn is a report_status tool call.
 *   2. Hold that LLM response so the run is provably blocked mid-turn.
 *   3. SIGTERM the server; wait until its log proves draining has engaged.
 *   4. Release the held response → the run pauses (status "paused",
 *      conversation persisted, task still locked) and the process exits
 *      cleanly (drain did not hang).
 *   5. Restart the binary against the same home → it resumes the paused
 *      run, runs the report_status tool call that was pending at pause time,
 *      makes its next LLM call (finish_task), and completes the task.
 *
 * The harness spawns the compiled binary directly so SIGTERM reaches the
 * server process and teardown can reap its complete process group.
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
    // server on its own SQLite database so its paused run can't be raced
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
            detached: true,
        });
        child.stdout?.on('data', (d) => { serverLog += d.toString(); });
        child.stderr?.on('data', (d) => { serverLog += d.toString(); });
        server = child;

        await expect
            .poll(async () => {
                try {
                    if (child.exitCode !== null || child.signalCode !== null) throw new Error(`isolated server exited (code=${child.exitCode}, signal=${child.signalCode})\n${serverLog}`);
                    return (await fetchWithTimeout(`${base}/api/ping`, {}, 2_000)).ok;
                } catch (err) {
                    if (child.exitCode !== null || child.signalCode !== null) throw err;
                    return false;
                }
            }, { timeout: 60_000, intervals: [500] })
            .toBe(true);
    }

    /** SIGTERM the server and resolve once the process has fully exited. */
    async function stopServer(): Promise<void> {
        const child = server;
        if (!child) return;
        await terminateProcess(child, { group: true, timeoutMs: 4_000 });
        server = null;
    }

    async function postJSON(url: string, data: unknown): Promise<any> {
        const res = await fetchWithTimeout(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data),
        }, 10_000);
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
        // Spawning the binary directly is what lets SIGTERM reach it.
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

    test('a paused run resumes and completes after restart', async () => {
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

        // ── Restart: the new process resumes the paused run ─────────────────
        const resumeMark = serverLog.length;
        await startServer();

        // Proof the resume path actually fired (not just that a fresh run ran).
        await expect
            .poll(() => /Resuming \d+ paused run/.test(serverLog.slice(resumeMark)),
                { timeout: 30_000, intervals: [200], message: 'restarted server should resume the paused run' })
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
        // run on resume — its side effect is the run's latest_reported_status.
        const finalRun = await (await fetch(`${base}/api/runs/${runId}`)).json();
        expect(finalRun.latest_reported_status).toBe(resumeMarker);

        // Exactly two LLM calls total across both processes: turn 1 (before
        // pause) and turn 2 (finish_task, after resume). The resumed
        // report_status executes locally, not via the LLM.
        const finalReqs = await (await fetch(`${mockUrl}/__test/requests`)).json();
        expect(finalReqs.completionsReceived).toBe(2);
    });

    test('all active runs pause and resume without duplicate runs or tools', async () => {
        test.setTimeout(240_000);
        const mockUrl = mock!.baseUrl;
        const markerA = 'run A resumed';
        const markerB = 'run B resumed';

        if (!server || server.exitCode !== null) await startServer();

        // The previous test leaves the isolated process running on its resumed
        // build. Reset both stores so this test proves that the startup scan
        // handles more than one paused session in the same restart.
        await resetE2EBase(base, mockUrl);
        await fetch(`${mockUrl}/__test/set-scenario`, {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                entries: [
                    { tool_call: { id: 'rs-a', name: 'report_status', arguments: { status: markerA } } },
                    { tool_call: { id: 'rs-b', name: 'report_status', arguments: { status: markerB } } },
                    { tool_call: { id: 'ft-a', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'Run A resumed.' } } },
                    { tool_call: { id: 'ft-b', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'Run B resumed.' } } },
                ],
            }),
        });

        const provider = await postJSON(`${base}/api/providers`, {
            name: 'mock', base_url: mockUrl, api_key: 'test-key',
            provider_type: 'openai', default_model: 'e2e-mock-model', supported_models: 'e2e-mock-model',
        });
        const company = await postJSON(`${base}/api/companies`, {
            name: 'Multi Resume Co', short_name: 'mrc', color: '#8b5cf6',
        });
        const sprint = await postJSON(`${base}/api/sprints`, {
            company_id: company.id, name: 'Sprint 1', goal: 'resume all runs',
        });
        const agent = await postJSON(`${base}/api/agents`, {
            company_id: company.id, name: 'Runner', system_prompt: 'You do the work.',
            model: 'e2e-mock-model', provider_id: provider.id,
        });
        const taskA = await postJSON(`${base}/api/tasks`, {
            company_id: company.id, sprint_id: sprint.id, agent_id: agent.id,
            title: 'Resumable task A', description: 'first concurrent resumable task', task_type: 'implement',
        });
        const taskB = await postJSON(`${base}/api/tasks`, {
            company_id: company.id, sprint_id: sprint.id, agent_id: agent.id,
            title: 'Resumable task B', description: 'second concurrent resumable task', task_type: 'implement',
        });

        // Hold both first LLM responses. This makes both runs active at the
        // exact moment SIGTERM begins draining, rather than relying on timing
        // between two ordinary completions.
        expect((await fetch(`${mockUrl}/__test/hold`, { method: 'POST' })).ok).toBeTruthy();
        await Promise.all([taskA, taskB].map((task) => fetch(`${base}/api/tasks/${task.id}`, {
            method: 'PUT', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ status: 'to-do' }),
        })));
        await expect
            .poll(async () => (await (await fetch(`${mockUrl}/__test/requests`)).json()).completionsReceived as number,
                { timeout: 30_000, intervals: [200], message: 'both runs should reach their held first LLM call' })
            .toBe(2);

        const runsA = await (await fetch(`${base}/api/tasks/${taskA.id}/runs`)).json();
        const runsB = await (await fetch(`${base}/api/tasks/${taskB.id}/runs`)).json();
        expect(runsA).toHaveLength(1);
        expect(runsB).toHaveLength(1);
        const runAId = runsA[0].id;
        const runBId = runsB[0].id;

        const drainMark = serverLog.length;
        const stopped = stopServer();
        await expect
            .poll(() => serverLog.slice(drainMark).includes('Draining active agent runs'),
                { timeout: 30_000, intervals: [100], message: 'server should begin draining both runs' })
            .toBe(true);
        expect((await fetch(`${mockUrl}/__test/release`, { method: 'POST' })).ok).toBeTruthy();
        await stopped;
        expect(server?.exitCode).toBe(0);
        expect((await (await fetch(`${mockUrl}/__test/requests`)).json()).completionsReceived).toBe(2);

        const resumeMark = serverLog.length;
        await startServer();
        await expect
            .poll(() => /Resuming 2 paused run/.test(serverLog.slice(resumeMark)),
                { timeout: 30_000, intervals: [200], message: 'restart should discover both paused runs' })
            .toBe(true);

        await expect
            .poll(async () => {
                const [a, b] = await Promise.all([
                    fetch(`${base}/api/tasks/${taskA.id}/runs`).then((r) => r.json()),
                    fetch(`${base}/api/tasks/${taskB.id}/runs`).then((r) => r.json()),
                ]);
                return [a.find((run: any) => run.id === runAId)?.status, b.find((run: any) => run.id === runBId)?.status];
            }, { timeout: 90_000, intervals: [500], message: 'both paused runs should complete after restart' })
            .toEqual(['completed', 'completed']);

        const [finalRunsA, finalRunsB] = await Promise.all([
            fetch(`${base}/api/tasks/${taskA.id}/runs`).then((r) => r.json()),
            fetch(`${base}/api/tasks/${taskB.id}/runs`).then((r) => r.json()),
        ]);
        expect(finalRunsA).toHaveLength(1);
        expect(finalRunsB).toHaveLength(1);
        expect(finalRunsA[0].id).toBe(runAId);
        expect(finalRunsB[0].id).toBe(runBId);
        // Concurrent activations may claim the scenario entries in either
        // order. Assert that both side effects survived, without coupling a
        // marker to whichever run happened to win the race for entry one.
        const statuses = await Promise.all([
            fetch(`${base}/api/runs/${runAId}`).then((r) => r.json()).then((run) => run.latest_reported_status),
            fetch(`${base}/api/runs/${runBId}`).then((r) => r.json()).then((run) => run.latest_reported_status),
        ]);
        expect(new Set(statuses)).toEqual(new Set([markerA, markerB]));

        // Two initial LLM calls plus two post-resume finish calls. The two
        // report_status side effects ran from restored pending tool calls, so
        // no tool was repeated and no extra model turn was generated.
        const finalRequests = await (await fetch(`${mockUrl}/__test/requests`)).json();
        expect(finalRequests.completionsReceived).toBe(4);
        const resumedRequests = finalRequests.requests
            .filter((request: any) => request.path.includes('/chat/completions'))
            .slice(-2);
        expect(resumedRequests).toHaveLength(2);
        expect(resumedRequests.every((request: any) =>
            request.body.messages.some((message: any) =>
                message.role === 'system' && String(message.content).includes('This session was resumed by')),
        )).toBe(true);
    });

    test('pauses before a multi-tool turn and resumes every side effect without history loss', async () => {
        test.setTimeout(240_000);
        const mockUrl = mock!.baseUrl;
        const marker = 'multi-tool resume status';

        // Keep the isolated process but reset its data and provider transcript.
        if (!server || server.exitCode !== null) await startServer();
        await resetE2EBase(base, mockUrl);
        const scenario = {
            entries: [
                {
                    tool_calls: [
                        { id: 'bash-1', name: 'bash', arguments: { command: 'printf BASH_RESUME_MARKER' } },
                        { id: 'write-1', name: 'write', arguments: { path: 'resume-tool-marker.txt', content: 'WRITE_RESUME_MARKER' } },
                        { id: 'read-1', name: 'read', arguments: { path: 'resume-tool-marker.txt' } },
                        { id: 'ls-1', name: 'ls', arguments: { path: '.', recursive: false } },
                        { id: 'grep-1', name: 'grep', arguments: { pattern: 'WRITE_RESUME_MARKER', path: 'resume-tool-marker.txt', recursive: false } },
                        { id: 'fetch-1', name: 'web_fetch', arguments: { url: `${mockUrl}/__test/health`, to_markdown: false } },
                        { id: 'browser-1', name: 'browser_use', arguments: { action: 'navigate', url: `${mockUrl}/__test/health` } },
                        { id: 'status-1', name: 'report_status', arguments: { status: marker } },
                    ],
                },
                { tool_call: { id: 'finish-1', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'All tools resumed.' } } },
            ],
        };
        expect((await fetch(`${mockUrl}/__test/set-scenario`, {
            method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(scenario),
        })).ok).toBeTruthy();

        const provider = await postJSON(`${base}/api/providers`, {
            name: 'multi-tool-mock', base_url: mockUrl, api_key: 'test-key',
            provider_type: 'openai', default_model: 'e2e-mock-model', supported_models: 'e2e-mock-model',
        });
        const company = await postJSON(`${base}/api/companies`, {
            name: 'Multi Tool Resume Co', short_name: 'mtr', color: '#14b8a6',
        });
        const sprint = await postJSON(`${base}/api/sprints`, {
            company_id: company.id, name: 'Sprint 1', goal: 'preserve every tool result',
        });
        const agent = await postJSON(`${base}/api/agents`, {
            company_id: company.id, name: 'Tool Runner', system_prompt: 'Use the requested tools.',
            model: 'e2e-mock-model', provider_id: provider.id,
        });
        const task = await postJSON(`${base}/api/tasks`, {
            company_id: company.id, sprint_id: sprint.id, agent_id: agent.id,
            title: 'Multi-tool resumable task', description: 'pause before tools', task_type: 'implement',
        });

        expect((await fetch(`${mockUrl}/__test/hold`, { method: 'POST' })).ok).toBeTruthy();
        const kick = await fetch(`${base}/api/tasks/${task.id}`, {
            method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ status: 'to-do' }),
        });
        expect(kick.ok).toBeTruthy();
        await expect
            .poll(async () => (await (await fetch(`${mockUrl}/__test/requests`)).json()).completionsReceived as number,
                { timeout: 30_000, intervals: [200], message: 'multi-tool run should reach its held first LLM call' })
            .toBe(1);

        const runs = await (await fetch(`${base}/api/tasks/${task.id}/runs`)).json();
        expect(runs).toHaveLength(1);
        const runId = runs[0].id;

        const drainMark = serverLog.length;
        const stopped = stopServer();
        await expect
            .poll(() => serverLog.slice(drainMark).includes('Draining active agent runs'),
                { timeout: 30_000, intervals: [100], message: 'server should begin draining the multi-tool run' })
            .toBe(true);
        expect((await fetch(`${mockUrl}/__test/release`, { method: 'POST' })).ok).toBeTruthy();
        await stopped;
        expect(server?.exitCode).toBe(0);
        expect((await (await fetch(`${mockUrl}/__test/requests`)).json()).completionsReceived).toBe(1);

        const resumeMark = serverLog.length;
        await startServer();
        await expect
            .poll(() => /Resuming 1 paused run/.test(serverLog.slice(resumeMark)),
                { timeout: 30_000, intervals: [200], message: 'restart should resume the multi-tool run' })
            .toBe(true);
        await expect
            .poll(async () => {
                const rs = await (await fetch(`${base}/api/tasks/${task.id}/runs`)).json();
                return rs.find((run: any) => run.id === runId)?.status ?? '';
            }, { timeout: 90_000, intervals: [500], message: 'multi-tool resumed run should complete' })
            .toBe('completed');

        const finalRun = await (await fetch(`${base}/api/runs/${runId}`)).json();
        expect(finalRun.latest_reported_status).toBe(marker);
        expect((await (await fetch(`${base}/api/tasks/${task.id}`)).json()).status).toBe('in-review');

        const finalRequests = await (await fetch(`${mockUrl}/__test/requests`)).json();
        expect(finalRequests.completionsReceived).toBe(2);
        const resumedRequest = finalRequests.requests
            .filter((request: any) => request.path.includes('/chat/completions'))
            .at(-1);
        expect(resumedRequest.body.messages.some((message: any) =>
            message.role === 'tool' && String(message.content).includes('BASH_RESUME_MARKER'))).toBe(true);
        expect(resumedRequest.body.messages.some((message: any) =>
            message.role === 'tool' && String(message.content).includes('WRITE_RESUME_MARKER'))).toBe(true);
        expect(resumedRequest.body.messages.some((message: any) =>
            message.role === 'tool' && String(message.content).includes('HTTP 200'))).toBe(true);
        expect(resumedRequest.body.messages.some((message: any) =>
            message.role === 'system' && String(message.content).includes('This session was resumed by'))).toBe(true);

        // The downloadable JSONL is the recovery source of truth. Verify the
        // paused assistant turn and every resumed tool result are present once
        // in that append-only trajectory (not merely in the provider request).
        const logResponse = await fetch(`${base}/api/runs/${runId}/log/download`);
        expect(logResponse.ok).toBeTruthy();
        const logLines = (await logResponse.text()).trim().split('\n').filter(Boolean).map((line) => JSON.parse(line));
        const messageEvents = logLines.filter((entry: any) => entry.type === 'message')
            .map((entry: any) => JSON.parse(entry.content));
        const pausedAssistant = messageEvents.find((message: any) =>
            message.role === 'assistant' && Array.isArray(message.tool_calls) &&
            message.tool_calls.some((call: any) => call.id === 'write-1'));
        expect(pausedAssistant).toBeTruthy();
        for (const callId of ['bash-1', 'write-1', 'read-1', 'ls-1', 'grep-1', 'fetch-1', 'browser-1', 'status-1']) {
            expect(messageEvents.filter((message: any) => message.role === 'tool' && message.tool_call_id === callId))
                .toHaveLength(1);
        }
    });
});
