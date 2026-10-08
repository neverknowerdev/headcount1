import { test, expect } from '@playwright/test';
import { spawn, spawnSync, ChildProcess } from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { startMockProviderServer } from '../fixtures/mock-provider-server';
import { terminateProcess, waitForExit } from '../helpers/process';
import { fetchWithTimeout } from '../helpers/http';
import { resetE2EBase } from '../helpers/reset';

/**
 * A task is never left stale by a restart.
 *
 * Everything a task waits on is stored on the task, not held by a goroutine,
 * so whatever the server was doing when it stopped, the next process finds it
 * and carries on. This spec proves that against a dedicated, isolated server
 * (its own home, SQLite database and port) that it can stop and kill freely:
 *
 *   - a planned restart (SIGTERM, as a deploy sends) pauses executor sessions
 *     at a turn boundary and the next process resumes those same sessions;
 *   - a crash (SIGKILL) in the middle of a smart model call loses nothing: the
 *     step never committed, so it is asked again;
 *   - a crash in the middle of an executor session is noticed (its heartbeat
 *     stops) and the work is retried;
 *   - a task waiting for the human is still waiting, for the same question.
 */
test.describe.serial('Workflow: restart and crash recovery', () => {
    const repoRoot = path.resolve(__dirname, '..', '..');
    const isPostgres = (process.env.DATABASE_URL || '').startsWith('postgres://');

    const port = 18500 + (process.pid % 500);
    const base = `http://localhost:${port}`;

    let home = '';
    let binPath = '';
    let mock: { baseUrl: string; port: number; stop: () => Promise<void> } | null = null;
    let server: ChildProcess | null = null;
    let serverLog = '';
    let workspace: { companyId: number; sprintId: number; providerId: number } | null = null;

    // The isolated server runs on its own SQLite database so nothing else can
    // pick its tasks up; a shared Postgres backend would break that.
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
                // Look at every unfinished task once a second, and take an
                // executor session for dead after a few seconds of silence.
                HEADCOUNT1_LIVENESS_INTERVAL: '1s',
                HEADCOUNT1_STALE_AFTER: '4s',
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
                    if (child.exitCode !== null || child.signalCode !== null) throw new Error(`isolated server exited (code=${child.exitCode}, signal=${child.signalCode})\n${serverLog.slice(-4000)}`);
                    return (await fetchWithTimeout(`${base}/api/ping`, {}, 2_000)).ok;
                } catch (err) {
                    if (child.exitCode !== null || child.signalCode !== null) throw err;
                    return false;
                }
            }, { timeout: 60_000, intervals: [500] })
            .toBe(true);
    }

    /** SIGTERM the server (a planned restart) and resolve once it has exited. */
    async function stopServer(): Promise<void> {
        if (server) await terminateProcess(server, { group: true, timeoutMs: 8_000 });
    }

    /** SIGKILL the server: no drain, no cleanup, as a crash or a power cut. */
    async function killServer(): Promise<void> {
        const child = server;
        if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return;
        try { process.kill(-child.pid, 'SIGKILL'); } catch { /* already gone */ }
        await waitForExit(child, 5_000);
    }

    async function api(method: string, pathname: string, data?: unknown): Promise<any> {
        const res = await fetchWithTimeout(`${base}${pathname}`, {
            method,
            headers: { 'Content-Type': 'application/json' },
            body: data === undefined ? undefined : JSON.stringify(data),
        }, 10_000);
        if (!res.ok) throw new Error(`${method} ${pathname} failed: ${res.status} ${await res.text().catch(() => '')}`);
        return res.json();
    }

    async function mockPost(pathname: string, data: unknown = {}): Promise<void> {
        const res = await fetch(`${mock!.baseUrl}${pathname}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data) });
        expect(res.ok).toBeTruthy();
    }

    const mockState = async (): Promise<any> => (await fetch(`${mock!.baseUrl}/__test/requests`)).json();

    /** Forget the provider's script and log; the server's data is kept. */
    async function resetMock(): Promise<void> {
        await mockPost('/__test/reset');
    }

    async function createTask(fields: Record<string, unknown>): Promise<any> {
        return api('POST', '/api/tasks', { company_id: workspace!.companyId, sprint_id: workspace!.sprintId, task_type: 'research', ...fields });
    }

    async function waitForTask(taskId: number, until: (task: any) => boolean, what: string, timeoutMs = 90_000): Promise<any> {
        let last: any = null;
        await expect.poll(async () => {
            try { last = await api('GET', `/api/tasks/${taskId}`); } catch { return false; }
            return until(last);
        }, { timeout: timeoutMs, intervals: [300], message: `task ${taskId} should become ${what}; last: ${JSON.stringify({ status: last?.status, phase: last?.phase, waiting_on: last?.waiting_on, detail: last?.wait_detail })}` }).toBe(true);
        return last;
    }

    test.beforeAll(async () => {
        if (isPostgres) return;
        home = fs.mkdtempSync(path.join(os.tmpdir(), 'hc1-restart-'));
        // Prefer the CI-prebuilt binary; otherwise build one now. Spawning the
        // binary directly is what lets signals reach it.
        const prebuilt = path.join(repoRoot, 'agent-orchestrator');
        if (fs.existsSync(prebuilt)) {
            binPath = prebuilt;
        } else {
            binPath = path.join(home, 'server-bin');
            const build = spawnSync('go', ['build', '-o', binPath, '.'], { cwd: repoRoot, encoding: 'utf8' });
            expect(build.status, `go build failed: ${build.stderr}`).toBe(0);
        }
        mock = await startMockProviderServer();
        await startServer();
        await resetE2EBase(base, mock.baseUrl);

        const provider = await api('POST', '/api/providers', {
            name: 'mock', base_url: mock.baseUrl, api_key: 'test-key', provider_type: 'openai',
            default_model: 'e2e-smart-model', supported_models: 'e2e-smart-model,e2e-cheap-model',
        });
        const company = await api('POST', '/api/companies', { name: 'Restart Co', short_name: 'restart-co', provider_id: provider.id, model: 'e2e-smart-model' });
        await api('PUT', '/api/default-model-settings/cheap', { provider_id: provider.id, model: 'e2e-cheap-model' });
        const sprint = await api('POST', '/api/sprints', { company_id: company.id, name: 'S1', goal: 'survive' });
        workspace = { companyId: company.id, sprintId: sprint.id, providerId: provider.id };
    });

    test.afterAll(async () => {
        await killServer();
        if (mock) await mock.stop();
        // A leaked temp dir is harmless; never fail teardown on it.
        if (home) {
            try { fs.rmSync(home, { recursive: true, force: true }); } catch { /* leak it */ }
        }
    });

    test('a planned restart pauses every executor session and the next process resumes them, each tool call run once', async () => {
        test.setTimeout(240_000);
        await resetMock();
        const first = await createTask({ title: 'First long job', description: 'Write two notes.' });
        const second = await createTask({ title: 'Second long job', description: 'Write two notes.' });

        // Each session's first turn is two tool calls with side effects; its
        // second turn reports. Everything else is the mock's autopilot.
        const session = (name: string) => ({
            match: { phase: 'executor', contains: name },
            replies: [
                { tool_calls: [
                    { name: 'write', arguments: { path: 'one.txt', content: `${name}: one` } },
                    { name: 'bash', arguments: { command: 'echo run >> runs.log' } },
                ] },
                { tool_call: { name: 'finish_work', arguments: { status: 'done', summary: `${name} survived the restart.` } } },
            ],
        });
        await mockPost('/__test/set-scenario', { rules: [session('First long job'), session('Second long job')] });
        await mockPost('/__test/hold', { match: { phase: 'executor' } });

        await api('PUT', `/api/tasks/${first.id}`, { status: 'to-do' });
        await api('PUT', `/api/tasks/${second.id}`, { status: 'to-do' });
        await expect.poll(async () => (await mockState()).held, { timeout: 60_000, intervals: [200], message: 'both sessions should be waiting on their first model call' }).toBe(2);

        const subtaskOf = async (taskId: number) => (await api('GET', `/api/tasks/${taskId}/tree`))[1];
        const runOf = async (taskId: number) => (await api('GET', `/api/tasks/${(await subtaskOf(taskId)).id}/runs`));
        const [runA, runB] = [(await runOf(first.id))[0], (await runOf(second.id))[0]];
        expect(runA.status).toBe('running');
        expect(runB.status).toBe('running');

        // ── The restart: draining must engage before the calls come back ──
        const drainMark = serverLog.length;
        const stopped = stopServer();
        await expect
            .poll(() => serverLog.slice(drainMark).includes('Draining active agent runs'), { timeout: 30_000, intervals: [100], message: 'the server should begin draining on SIGTERM' })
            .toBe(true);
        // Releasing the held responses lets each session receive its tool
        // calls and pause at that boundary instead of running them.
        await mockPost('/__test/release');
        await stopped;
        expect(server?.exitCode, 'the drain finished on its own; the process was not killed').toBe(0);
        const executorCalls = async () => ((await mockState()).completions as any[]).filter((entry) => entry.phase === 'executor').length;
        expect(await executorCalls(), 'no session went on to a second turn').toBe(2);

        // ── The next process resumes the same sessions ──
        await startServer();
        await waitForTask(first.id, (task) => task.status === 'in-review', 'in review after the restart');
        await waitForTask(second.id, (task) => task.status === 'in-review', 'in review after the restart');

        for (const [taskId, run, name] of [[first.id, runA, 'First long job'], [second.id, runB, 'Second long job']] as const) {
            const runs = await runOf(taskId);
            expect(runs, 'the session was resumed, not started over').toHaveLength(1);
            expect(runs[0].id).toBe(run.id);
            expect(runs[0].status).toBe('completed');
            expect(runs[0].attempt).toBe(1);
            // The tool calls pending at the pause ran after the restart, once.
            // What the tools answered is what the model was told next.
            const told = ((await mockState()).completions as any[])
                .filter((entry) => entry.phase === 'executor' && JSON.stringify(entry.body.messages).includes(name))
                .flatMap((entry) => entry.body.messages.filter((message: any) => message.role === 'tool').map((message: any) => String(message.content)));
            const files = fs.readdirSync(runs[0].workspace_path);
            expect(files, `tool results: ${JSON.stringify(told)}`).toEqual(expect.arrayContaining(['one.txt', 'runs.log']));
            expect(fs.readFileSync(path.join(runs[0].workspace_path, 'one.txt'), 'utf8')).toBe(`${name}: one`);
            expect(fs.readFileSync(path.join(runs[0].workspace_path, 'runs.log'), 'utf8').trim().split('\n')).toEqual(['run']);
            expect((await subtaskOf(taskId)).result_summary).toBe(`${name} survived the restart.`);
        }
        // Two turns per session across both processes: nothing was asked twice.
        expect(await executorCalls()).toBe(4);
    });

    test('a crash during a smart model call loses nothing: the step is asked again', async () => {
        test.setTimeout(240_000);
        await resetMock();
        const task = await createTask({ title: 'Crashes while thinking', description: 'The server dies during refinement.' });
        await mockPost('/__test/hold', { match: { phase: 'refine' } });

        await api('PUT', `/api/tasks/${task.id}`, { status: 'to-do' });
        await expect.poll(async () => (await mockState()).held, { timeout: 30_000, intervals: [200] }).toBe(1);
        const during = await api('GET', `/api/tasks/${task.id}`);
        expect(during.status).toBe('in-progress');
        expect(during.phase).toBe('refine');

        await killServer();
        // The answer to the interrupted call goes nowhere.
        await mockPost('/__test/release');

        await startServer();
        // The dead process still holds the task's lease; once it lapses the
        // sweep finds the task exactly where it was and redoes the step.
        await waitForTask(task.id, (t) => t.status === 'in-review', 'in review after the crash', 150_000);

        const refineCalls = ((await mockState()).completions as any[]).filter((entry) => entry.phase === 'refine');
        expect(refineCalls, 'the interrupted step was asked again').toHaveLength(2);
        const steps = await api('GET', `/api/tasks/${task.id}/steps`);
        // The journal holds the step once: the first attempt never committed.
        expect(steps.filter((step: any) => step.kind === 'smart_call').map((step: any) => step.tool_name))
            .toEqual(['finish_refinement', 'create_tasks', 'finish_verification']);
    });

    test('a crash during an executor session is noticed and the work is retried', async () => {
        test.setTimeout(240_000);
        await resetMock();
        const task = await createTask({ title: 'Crashes while working', description: 'The server dies during execution.' });
        await mockPost('/__test/hold', { match: { phase: 'executor' } });

        await api('PUT', `/api/tasks/${task.id}`, { status: 'to-do' });
        await expect.poll(async () => (await mockState()).held, { timeout: 60_000, intervals: [200] }).toBe(1);
        const subtask = (await api('GET', `/api/tasks/${task.id}/tree`))[1];
        expect(subtask.waiting_on).toBe('run');

        await killServer();
        await mockPost('/__test/release');
        await startServer();

        await waitForTask(task.id, (t) => t.status === 'in-review', 'in review after the crash', 150_000);
        const runs = (await api('GET', `/api/tasks/${subtask.id}/runs`)).sort((a: any, b: any) => a.id - b.id);
        expect(runs.map((run: any) => [run.attempt, run.status])).toEqual([[1, 'failed'], [2, 'completed']]);
        expect((await api('GET', `/api/tasks/${subtask.id}`)).status).toBe('done');
    });

    test('a task waiting for the human is still waiting after a restart, and resumes on the answer', async () => {
        test.setTimeout(180_000);
        await resetMock();
        const task = await createTask({ title: 'Asks before it starts', description: 'Needs a decision.' });
        await mockPost('/__test/set-scenario', { rules: [{ match: { phase: 'refine' }, replies: [
            { tool_call: { name: 'ask_human', arguments: {
                question: 'Ship on Friday or Monday?', why: 'Only you can decide.',
                decisions: [{ title: 'Ask for the ship day', decision: 'Ask', rationale: 'It is a business call.' }],
            } } },
        ] }] });

        await api('PUT', `/api/tasks/${task.id}`, { status: 'to-do' });
        const blocked = await waitForTask(task.id, (t) => t.status === 'blocked' && t.waiting_on === 'human', 'blocked on the human');

        await stopServer();
        await startServer();

        const after = await api('GET', `/api/tasks/${task.id}`);
        expect([after.status, after.waiting_on, after.wait_ref]).toEqual(['blocked', 'human', blocked.wait_ref]);
        // Nothing was asked of the provider again while it waited.
        expect((await mockState()).completionsReceived).toBe(1);

        const question = (await api('GET', `/api/comments?task_id=${task.id}`)).find((comment: any) => comment.comment_type === 'ask_user');
        expect(question.content).toContain('Ship on Friday or Monday?');
        await api('POST', '/api/comments', { task_id: task.id, author_type: 'human', content: 'Monday.', reply_to_id: question.id });
        await waitForTask(task.id, (t) => t.status === 'in-review', 'in review once answered');
    });
});
