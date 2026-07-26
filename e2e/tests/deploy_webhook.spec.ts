import { test, expect } from '@playwright/test';
import { spawn, spawnSync, ChildProcess } from 'child_process';
import * as crypto from 'crypto';
import * as http from 'http';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { AddressInfo } from 'net';
import { startMockProviderServer } from '../fixtures/mock-provider-server';

/**
 * Server-side deploy webhook, end-to-end.
 *
 * CI notifies a running server at POST /api/deploy/webhook; when the event
 * matches the server's environment/settings, the server downloads the built
 * binary, verifies its digest, swaps its own executable, and — after draining
 * in-flight agent runs and sealing its keyring — execs into the new build.
 *
 * Two scenarios are covered against a real, isolated staging server:
 *   1. Guards + a plain deploy: bad key, artifact from a disallowed host,
 *      tampered digest, wrong-environment event, then a valid deploy that
 *      actually restarts the server into a differently-versioned binary.
 *   2. A deploy WHILE an agent run is mid-turn — the composition of the deploy
 *      and drain/resume features. This is the case that must not lose the run
 *      or leave the port unserved.
 */
test.describe.serial('Deploy webhook', () => {
    const repoRoot = path.resolve(__dirname, '..', '..');
    const isPostgres = (process.env.DATABASE_URL || '').startsWith('postgres://');

    const port = 18700 + (process.pid % 300);
    const base = `http://localhost:${port}`;
    const deployKey = 'e2e-deploy-key';
    const runningCommit = 'aaaaaa1'; // binary A, what each test starts on
    const targetCommit = 'e2edep0';  // binary B, the deploy target

    let home = '';
    let pristineBinA = ''; // rebuilt-from-source binary A, copied in before each test
    let serverBinPath = ''; // the path the server runs from (a deploy overwrites it)
    let targetBinPath = ''; // binary B, served as the "release asset"
    let targetSha256 = '';  // digest of binary B; the server pins the artifact by it
    let assetServer: http.Server | null = null;
    let assetBaseUrl = '';
    let mock: { baseUrl: string; port: number; stop: () => Promise<void> } | null = null;
    let server: ChildProcess | null = null;
    let serverLog = '';

    test.skip(isPostgres, 'isolated-SQLite test: incompatible with a shared Postgres backend');

    function buildBinary(outPath: string, commit: string): void {
        const build = spawnSync('go', ['build',
            '-ldflags', `-X main.CommitHash=${commit} -X main.BuildDate=2026-01-01 -X main.Branch=deploy-e2e`,
            '-o', outPath, 'main.go',
        ], { cwd: repoRoot, encoding: 'utf8' });
        expect(build.status, `go build failed: ${build.stderr}`).toBe(0);
    }

    async function startServer(): Promise<void> {
        const child = spawn(serverBinPath, [], {
            cwd: repoRoot,
            env: {
                ...process.env,
                DATABASE_URL: '',
                E2E_MODE: 'true',
                E2E_HEADCOUNT1_HOME: home,
                PORT: String(port),
                HEADCOUNT1_ENV: 'staging',
                HEADCOUNT1_DEPLOY_API_KEY: deployKey,
                // The fixture asset server is local, so allow it explicitly —
                // this exercises the operator-mirror path of the host allowlist
                // rather than bypassing the validation.
                HEADCOUNT1_DEPLOY_ALLOWED_HOSTS: '127.0.0.1',
            },
            stdio: ['ignore', 'pipe', 'pipe'],
        });
        child.stdout?.on('data', (d) => { serverLog += d.toString(); });
        child.stderr?.on('data', (d) => { serverLog += d.toString(); });
        server = child;
        await expect
            .poll(async () => { try { return (await fetch(`${base}/api/ping`)).ok; } catch { return false; } },
                { timeout: 60_000, intervals: [500] })
            .toBe(true);
    }

    async function stopServer(): Promise<void> {
        const child = server;
        if (child && child.exitCode === null) {
            const exited = new Promise<void>((resolve) => child.once('exit', () => resolve()));
            child.kill('SIGTERM');
            await exited;
        }
        server = null;
        // A deploy execs in place so the PID is unchanged and the kill above is
        // enough. This is a belt-and-braces reap for a run that failed midway;
        // serverBinPath sits under a temp home unique to this spec, so it can
        // only ever match our own process.
        if (serverBinPath) spawnSync('pkill', ['-9', '-f', serverBinPath]);
    }

    async function version(): Promise<string> {
        const res = await fetch(`${base}/api/version`);
        if (!res.ok) return '';
        return (await res.json()).commit_hash ?? '';
    }

    function deployPost(key: string, payload: unknown): Promise<Response> {
        return fetch(`${base}/api/deploy/webhook`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'X-Deploy-Key': key },
            body: JSON.stringify(payload),
        });
    }

    async function postJSON(url: string, data: unknown): Promise<any> {
        const res = await fetch(url, {
            method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data),
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
        home = fs.mkdtempSync(path.join(os.tmpdir(), 'hc1-deploy-'));

        // Binary A: what the server runs before each deploy. Kept pristine and
        // copied into place per test, since a deploy overwrites the live path.
        pristineBinA = path.join(home, 'server-bin-a');
        buildBinary(pristineBinA, runningCommit);
        serverBinPath = path.join(home, 'server-bin');

        // Binary B: the deploy target, a distinct version, served as an asset.
        targetBinPath = path.join(home, 'agent-orchestrator-linux-amd64');
        buildBinary(targetBinPath, targetCommit);
        targetSha256 = crypto.createHash('sha256').update(fs.readFileSync(targetBinPath)).digest('hex');

        assetServer = http.createServer((req, res) => {
            if (req.url?.includes('agent-orchestrator')) {
                res.writeHead(200, { 'Content-Type': 'application/octet-stream' });
                res.end(fs.readFileSync(targetBinPath));
            } else {
                res.writeHead(404); res.end();
            }
        });
        await new Promise<void>((resolve) => assetServer!.listen(0, '127.0.0.1', () => resolve()));
        assetBaseUrl = `http://127.0.0.1:${(assetServer!.address() as AddressInfo).port}`;

        mock = await startMockProviderServer();
    });

    test.beforeEach(async () => {
        if (isPostgres) return;
        // Every test starts on binary A, whatever the previous test deployed.
        fs.copyFileSync(pristineBinA, serverBinPath);
        fs.chmodSync(serverBinPath, 0o755);
        if (mock) await fetch(`${mock.baseUrl}/__test/reset`, { method: 'POST' });
    });

    test.afterEach(async () => {
        await stopServer();
    });

    test.afterAll(async () => {
        await stopServer();
        if (assetServer) await new Promise<void>((r) => assetServer!.close(() => r()));
        if (mock) await mock.stop();
        if (home) { try { fs.rmSync(home, { recursive: true, force: true }); } catch { /* leak */ } }
    });

    /** A valid staging branch-deploy event for binary B. */
    function branchEvent() {
        return {
            event_type: 'branch', ref: 'feature-x', commit: targetCommit,
            build_date: '2026-01-01',
            download_url: `${assetBaseUrl}/agent-orchestrator-linux-amd64`,
            sha256: targetSha256, target: 'staging',
        };
    }

    test('rejects untrusted events and deploys a valid one', async () => {
        test.setTimeout(240_000);
        await startServer();

        expect(await version()).toBe(runningCommit);

        // Wrong key is rejected — no deploy.
        expect((await deployPost('nope', branchEvent())).status).toBe(401);

        // An artifact from outside the allowed deploy sources is refused even
        // WITH the right key: the shared secret must not be enough to make the
        // server execute arbitrary code.
        const foreign = await deployPost(deployKey, {
            ...branchEvent(), download_url: 'https://evil.example.com/backdoor',
        });
        expect(foreign.status).toBe(400);

        // A missing digest is refused: it would otherwise allow deploying any
        // artifact ever published, including an old vulnerable build.
        const noDigest = await deployPost(deployKey, { ...branchEvent(), sha256: '' });
        expect(noDigest.status).toBe(400);

        // A tampered artifact (right URL, wrong digest) is downloaded but
        // rejected before the binary is swapped. The webhook has already been
        // answered by then, so wait for the failure the server recorded.
        expect((await deployPost(deployKey, { ...branchEvent(), sha256: 'f'.repeat(64) })).status).toBe(202);
        await expect
            .poll(async () => {
                const st = await (await fetch(`${base}/api/deploy/status`)).json();
                return st.last_error ?? '';
            }, { timeout: 60_000, intervals: [500], message: 'digest mismatch should be recorded' })
            .toContain('sha256 mismatch');
        expect(await version()).toBe(runningCommit);

        // A production-only event (main) is ignored by this staging server.
        const ignored = await deployPost(deployKey, {
            ...branchEvent(), event_type: 'main', ref: 'main', target: 'production',
        });
        expect(ignored.status).toBe(200);
        expect((await ignored.json()).status).toBe('ignored');
        expect(await version()).toBe(runningCommit);

        // The real thing: a branch event this staging server accepts.
        const accepted = await deployPost(deployKey, branchEvent());
        expect(accepted.status).toBe(202);
        expect((await accepted.json()).status).toBe('deploying');

        // The server verifies + swaps the binary, drains (nothing in flight),
        // and execs in place — so /api/version reporting binary B's commit means
        // the deploy completed across a real restart.
        await expect
            .poll(async () => { try { return await version(); } catch { return ''; } },
                { timeout: 90_000, intervals: [1000], message: 'server should restart into the deployed binary' })
            .toBe(targetCommit);

        expect((await fetch(`${base}/api/ping`)).ok).toBeTruthy();
    });

    // The composition of the two features: deploying while an agent run is
    // mid-turn. Before the exec-on-shutdown fix this failed two ways — the
    // successor raced the draining process for the port and died, and its
    // resume scan ran before the paused run had been written, stranding it.
    test('a deploy during an in-flight run drains it and resumes it in the new build', async () => {
        test.setTimeout(240_000);
        const mockUrl = mock!.baseUrl;
        const statusMarker = 'progress recorded before the deploy';

        await startServer();
        await fetch(`${base}/api/e2e/wipe-db`, { method: 'POST' });
        expect(await version()).toBe(runningCommit);

        // Seed a runnable task on this isolated server.
        const provider = await postJSON(`${base}/api/providers`, {
            name: 'mock', base_url: mockUrl, api_key: 'test-key',
            provider_type: 'openai', default_model: 'e2e-mock-model', supported_models: 'e2e-mock-model',
        });
        const company = await postJSON(`${base}/api/companies`, { name: 'Deploy Co', short_name: 'dc', color: '#0ea5e9' });
        const sprint = await postJSON(`${base}/api/sprints`, { company_id: company.id, name: 'S1', goal: 'ship' });
        const agent = await postJSON(`${base}/api/agents`, {
            company_id: company.id, name: 'Runner', system_prompt: 'You do the work.',
            model: 'e2e-mock-model', provider_id: provider.id,
        });
        const task = await postJSON(`${base}/api/tasks`, {
            company_id: company.id, sprint_id: sprint.id, agent_id: agent.id,
            title: 'Survives a deploy', description: 'a task to deploy through', task_type: 'implement',
        });

        // Turn 1 reports progress (a tool call, so the run has more to do and is
        // a valid pause point); turn 2 finishes after the restart.
        await fetch(`${mockUrl}/__test/set-scenario`, {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                entries: [
                    { tool_call: { id: 'rs-1', name: 'report_status', arguments: { status: statusMarker } } },
                    { tool_call: { id: 'ft-1', name: 'finish_task', arguments: { task_status: 'in-review', finish_status: 'Survived the deploy.' } } },
                ],
            }),
        });
        // Hold the LLM response so the run is provably blocked mid-turn.
        await fetch(`${mockUrl}/__test/hold`, { method: 'POST' });

        await fetch(`${base}/api/tasks/${task.id}`, {
            method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ status: 'to-do' }),
        });
        await expect
            .poll(async () => (await (await fetch(`${mockUrl}/__test/requests`)).json()).completionsReceived as number,
                { timeout: 30_000, intervals: [200], message: 'run should reach its first LLM call' })
            .toBeGreaterThanOrEqual(1);

        const runs = await (await fetch(`${base}/api/tasks/${task.id}/runs`)).json();
        expect(runs.length).toBe(1);
        const runId = runs[0].id;

        // Deploy while that run is blocked on its LLM call.
        const drainMark = serverLog.length;
        const accepted = await deployPost(deployKey, branchEvent());
        expect(accepted.status).toBe(202);

        // The server must drain rather than exit or hand the port over early.
        await expect
            .poll(() => serverLog.slice(drainMark).includes('Draining active agent runs'),
                { timeout: 60_000, intervals: [200], message: 'deploy should drain before restarting' })
            .toBe(true);
        // Still serving on the old build: the successor has NOT been started yet,
        // so nothing is competing for the port.
        expect(await version()).toBe(runningCommit);

        // Keep the run blocked well past listenWithRetry's 10s bind deadline
        // before letting it finish. This is what makes the test cover the
        // port-bind failure mode too: if a change ever reintroduces spawning the
        // successor up front, it will exhaust that deadline against the still-
        // draining process and die, and the assertions below will fail. A short
        // drain would let such a regression slip through unnoticed.
        await new Promise((r) => setTimeout(r, 14_000));
        // The old build is still the one serving throughout the drain.
        expect(await version()).toBe(runningCommit);

        // Let the held turn complete → the run pauses at the turn boundary and
        // the drain finishes, freeing the process to exec the new binary.
        await fetch(`${mockUrl}/__test/release`, { method: 'POST' });

        await expect
            .poll(async () => { try { return await version(); } catch { return ''; } },
                { timeout: 120_000, intervals: [1000], message: 'deploy should restart into the new build' })
            .toBe(targetCommit);

        // The new build picks the interrupted run back up and finishes it.
        await expect
            .poll(async () => {
                try {
                    const rs = await (await fetch(`${base}/api/tasks/${task.id}/runs`)).json();
                    return rs.find((r: any) => r.id === runId)?.status ?? '';
                } catch { return ''; }
            }, { timeout: 120_000, intervals: [1000], message: 'the interrupted run must resume in the new build and complete' })
            .toBe('completed');

        // The tool call pending at pause time ran on resume, and the task
        // reached the status the agent set after the restart.
        const finalRun = await (await fetch(`${base}/api/runs/${runId}`)).json();
        expect(finalRun.current_status).toBe(statusMarker);
        const finalTask = await (await fetch(`${base}/api/tasks/${task.id}`)).json();
        expect(finalTask.status).toBe('in-review');
    });
});
