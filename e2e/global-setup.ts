import { FullConfig } from '@playwright/test';
import { execFileSync, spawn, ChildProcess } from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { createE2EHome } from './fixtures/e2e-home';
import { setupBareRepo } from './fixtures/git-fixture';
import { startMockProviderServer } from './fixtures/mock-provider-server';
import { fetchWithTimeout, requireFetchOK } from './helpers/http';
import { terminateProcess } from './helpers/process';

const runID = `${process.pid}-${Date.now()}`;
const runDir = fs.mkdtempSync(path.join(os.tmpdir(), `headcount1-e2e-${runID}-`));
// Playwright loads test modules before globalSetup, so the default metadata
// paths must be discoverable without setup having mutated process.env yet.
// They are overwritten at the start of every run and removed by teardown.
const envFile = process.env.E2E_ENV_FILE || path.join(__dirname, '.e2e-env.json');
const pidFile = process.env.E2E_PID_FILE || path.join(__dirname, '.e2e-server.pid');
const logFile = path.join(runDir, 'server.log');

let serverProcess: ChildProcess | null = null;
let mock: Awaited<ReturnType<typeof startMockProviderServer>> | null = null;
let log = '';

/** Playwright global setup with bounded startup and failure cleanup. */
export default async function globalSetup(config: FullConfig): Promise<void> {
    const baseURL = config.projects[0]?.use?.baseURL || 'http://localhost:8080';
    const port = new URL(baseURL).port || '80';
    let e2eHome = '';
    try {
        e2eHome = createE2EHome();
        const repoUrl = setupBareRepo();
        mock = await startMockProviderServer();

        const envData = {
            E2E_MOCK_PROVIDER_URL: mock.baseUrl,
            E2E_TEST_REPO_URL: repoUrl,
            E2E_HEADCOUNT1_HOME: e2eHome,
            E2E_ENV_FILE: envFile,
            E2E_PID_FILE: pidFile,
            E2E_SERVER_LOG: logFile,
            E2E_RUN_DIR: runDir,
        };
        fs.writeFileSync(envFile, JSON.stringify(envData, null, 2));
        Object.assign(process.env, envData, { E2E_MODE: 'true' });

        const projectRoot = path.resolve(__dirname, '..');
        const prebuiltBinary = path.join(projectRoot, 'agent-orchestrator');
        let binary = prebuiltBinary;
        if (!fs.existsSync(binary)) {
            binary = path.join(runDir, 'agent-orchestrator');
            console.log(`[globalSetup] building server binary at ${binary}`);
            execFileSync('go', ['build', '-o', binary, '.'], {
                cwd: projectRoot,
                env: process.env,
                stdio: 'pipe',
                timeout: 120_000,
            });
        }
        const env: Record<string, string> = { ...(process.env as Record<string, string>), ...envData };
        env.E2E_MODE = 'true';
        env.PORT = port;
        env.E2E_HEADCOUNT1_HOME = e2eHome;

        console.log(`[globalSetup] starting server via ${binary}`);
        const child = spawn(binary, [], {
            cwd: projectRoot,
            env,
            detached: true,
            stdio: ['ignore', 'pipe', 'pipe'],
        });
        child.stdout?.on('data', (data) => appendLog(`[stdout] ${data}`));
        child.stderr?.on('data', (data) => appendLog(`[stderr] ${data}`));
        child.on('exit', (code, signal) => appendLog(`[exit] code=${code} signal=${signal}`));
        child.on('error', (err) => appendLog(`[error] ${err.message}`));
        serverProcess = child;
        if (!child.pid) throw new Error('globalSetup: server process did not expose a pid');
        fs.writeFileSync(pidFile, JSON.stringify({ pid: child.pid, group: true, logFile }));

        await waitForServer(baseURL, child);
        await waitForSetup(baseURL, child);
        // Wipe also reseeds built-in integrations and can take longer than
        // the lightweight health/setup probes, especially on a cold E2E
        // workspace. Keep this aligned with the reset helper's budget so a
        // healthy server is not torn down while the database is initializing.
        await requireFetchOK(`${baseURL}/api/e2e/wipe-db`, { method: 'POST' }, 65_000);
        console.log('[globalSetup] wiped database via /api/e2e/wipe-db');
    } catch (err) {
        await cleanupSetup(e2eHome);
        throw err;
    }
}

function appendLog(message: string): void {
    log = `${log}${message}`.slice(-50_000);
    try { fs.appendFileSync(logFile, message); } catch { /* best effort */ }
    process.stdout.write(`[server] ${message}`);
}

async function cleanupSetup(e2eHome: string): Promise<void> {
    if (serverProcess) await terminateProcess(serverProcess, { group: true, timeoutMs: 3_000 });
    if (mock) await mock.stop();
    if (e2eHome) {
        try { fs.rmSync(e2eHome, { recursive: true, force: true }); } catch { /* best effort */ }
    }
    try { fs.writeFileSync(logFile, log); } catch { /* best effort */ }
    try { fs.unlinkSync(pidFile); } catch { /* best effort */ }
    try { fs.unlinkSync(envFile); } catch { /* best effort */ }
}

async function waitForServer(
    url: string,
    child: ChildProcess,
    timeoutMs = 120_000,
): Promise<void> {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        if (child.exitCode !== null || child.signalCode !== null) {
            throw new Error(`globalSetup: server exited before readiness (code=${child.exitCode}, signal=${child.signalCode})\n${log}`);
        }
        try {
            const res = await fetchWithTimeout(`${url}/api/ping`, {}, Math.min(2_000, deadline - Date.now()));
            if (res.ok) return;
        } catch { /* retry until deadline or child exit */ }
        await new Promise((resolve) => setTimeout(resolve, 250));
    }
    throw new Error(`globalSetup: server at ${url} did not become reachable within ${timeoutMs}ms\n${log}`);
}

async function waitForSetup(
    url: string,
    child: ChildProcess,
    timeoutMs = 180_000,
): Promise<void> {
    const deadline = Date.now() + timeoutMs;
    let lastStatus = 'pending';
    while (Date.now() < deadline) {
        if (child.exitCode !== null || child.signalCode !== null) {
            throw new Error(`globalSetup: server exited during dependency setup (code=${child.exitCode}, signal=${child.signalCode})\n${log}`);
        }
        try {
            const res = await fetchWithTimeout(`${url}/api/setup-status`, {}, Math.min(2_000, deadline - Date.now()));
            if (res.ok) {
                const data = await res.json() as { pending?: boolean; ok?: boolean; error?: string; failures?: Array<{ name?: string; reason?: string }> };
                if (data.pending) lastStatus = data.error || 'pending';
                else if (data.ok) return;
                else {
                    const failures = (data.failures || []).map((f) => [f.name, f.reason].filter(Boolean).join(': ')).filter(Boolean).join('; ');
                    throw new Error(`globalSetup: dependency setup failed${failures ? ` (${failures})` : ''}${data.error ? `: ${data.error}` : ''}`);
                }
            }
        } catch (err) {
            if (err instanceof Error && err.message.startsWith('globalSetup: dependency setup failed')) throw err;
            lastStatus = 'setup-status unavailable';
        }
        await new Promise((resolve) => setTimeout(resolve, 250));
    }
    throw new Error(`globalSetup: dependency setup did not finish within ${timeoutMs}ms (last status: ${lastStatus})\n${log}`);
}
