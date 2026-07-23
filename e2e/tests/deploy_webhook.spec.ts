import { test, expect } from '@playwright/test';
import { spawn, spawnSync, ChildProcess } from 'child_process';
import * as http from 'http';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { AddressInfo } from 'net';

/**
 * Server-side deploy webhook, end-to-end.
 *
 * CI notifies a running server at POST /api/deploy/webhook; when the event
 * matches the server's environment/settings, the server downloads the built
 * binary, swaps its own executable, and restarts into the new build (reusing
 * the drain/resume graceful-restart flow).
 *
 * This spec drives that whole path against a real, isolated staging server:
 *   1. Start the server from binary A (a distinctly-versioned build).
 *   2. POST a branch deploy event pointing at binary B (a different build)
 *      served over a local HTTP file server, authenticated with the deploy key.
 *   3. Assert the server self-replaces and comes back reporting binary B's
 *      version — i.e. the deploy actually took effect across a real restart.
 * It also checks the auth/decision guards: a bad key is rejected, and a
 * production-only event is ignored by this staging server.
 */
test.describe.serial('Deploy webhook triggers a self-replacing restart', () => {
    const repoRoot = path.resolve(__dirname, '..', '..');
    const isPostgres = (process.env.DATABASE_URL || '').startsWith('postgres://');

    const port = 18700 + (process.pid % 300);
    const base = `http://localhost:${port}`;
    const deployKey = 'e2e-deploy-key';
    const targetCommit = 'e2edep0'; // binary B's compiled-in commit

    let home = '';
    let serverBinPath = ''; // binary A, the path the server runs from (gets overwritten)
    let targetBinPath = ''; // binary B, served as the "release asset"
    let assetServer: http.Server | null = null;
    let assetBaseUrl = '';
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
        // After a deploy the original process has already exited and the
        // restarted binary runs reparented (not tracked by `server`). It still
        // runs from serverBinPath, whose temp `home` prefix is unique to this
        // test, so pkill by path reliably reaps it without touching anything else.
        if (serverBinPath) {
            spawnSync('pkill', ['-9', '-f', serverBinPath]);
        }
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

    test.beforeAll(async () => {
        if (isPostgres) return;
        home = fs.mkdtempSync(path.join(os.tmpdir(), 'hc1-deploy-'));

        // Binary A: the running server. Copy to a writable path we own, since
        // the deploy overwrites os.Executable() in place.
        serverBinPath = path.join(home, 'server-bin');
        buildBinary(serverBinPath, 'aaaaaa1');

        // Binary B: the deploy target, a distinct version, served as an asset.
        targetBinPath = path.join(home, 'agent-orchestrator-linux-amd64');
        buildBinary(targetBinPath, targetCommit);

        assetServer = http.createServer((req, res) => {
            if (req.url?.includes('agent-orchestrator')) {
                const buf = fs.readFileSync(targetBinPath);
                res.writeHead(200, { 'Content-Type': 'application/octet-stream' });
                res.end(buf);
            } else {
                res.writeHead(404); res.end();
            }
        });
        await new Promise<void>((resolve) => assetServer!.listen(0, '127.0.0.1', () => resolve()));
        assetBaseUrl = `http://127.0.0.1:${(assetServer!.address() as AddressInfo).port}`;
    });

    test.afterAll(async () => {
        await stopServer();
        if (assetServer) await new Promise<void>((r) => assetServer!.close(() => r()));
        if (home) { try { fs.rmSync(home, { recursive: true, force: true }); } catch { /* leak */ } }
    });

    test('a matching branch event self-replaces the binary and restarts into it', async () => {
        test.setTimeout(180_000);
        await startServer();

        // Sanity: running binary A.
        expect(await version()).toBe('aaaaaa1');

        const downloadURL = `${assetBaseUrl}/agent-orchestrator-linux-amd64`;

        // Wrong key is rejected — no deploy.
        const badKey = await deployPost('nope', {
            event_type: 'branch', ref: 'feature-x', commit: targetCommit,
            build_date: '2026-01-01', download_url: downloadURL, target: 'staging',
        });
        expect(badKey.status).toBe(401);

        // A production-only event (main) is ignored by this staging server.
        const ignored = await deployPost(deployKey, {
            event_type: 'main', ref: 'main', commit: targetCommit,
            build_date: '2026-01-01', download_url: downloadURL, target: 'production',
        });
        expect(ignored.status).toBe(200);
        expect((await ignored.json()).status).toBe('ignored');
        // Still binary A — nothing was applied.
        expect(await version()).toBe('aaaaaa1');

        // The real thing: a branch event this staging server accepts.
        const accepted = await deployPost(deployKey, {
            event_type: 'branch', ref: 'feature-x', commit: targetCommit,
            build_date: '2026-01-01', download_url: downloadURL, target: 'staging',
        });
        expect(accepted.status).toBe(202);
        expect((await accepted.json()).status).toBe('deploying');

        // The server downloads binary B, swaps its executable, and restarts.
        // Its process re-execs in place, so once /api/version reports binary B's
        // commit the self-replacing deploy has completed across a real restart.
        await expect
            .poll(async () => { try { return await version(); } catch { return ''; } },
                { timeout: 90_000, intervals: [1000], message: 'server should restart into the deployed binary' })
            .toBe(targetCommit);

        // The restarted server is healthy.
        expect((await fetch(`${base}/api/ping`)).ok).toBeTruthy();
    });
});
