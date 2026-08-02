import { FullConfig } from '@playwright/test';
import { spawn, ChildProcessWithoutNullStreams } from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { createE2EHome } from './fixtures/e2e-home';
import { setupBareRepo } from './fixtures/git-fixture';
import { startMockProviderServer } from './fixtures/mock-provider-server';
import { startMockDeployServer } from './fixtures/mock-deploy-server';

const envFile = process.env.E2E_ENV_FILE || path.join(__dirname, '.e2e-env.json');
const pidFile = process.env.E2E_PID_FILE || path.join(__dirname, '.e2e-server.pid');

let serverProcess: ChildProcessWithoutNullStreams | null = null;

/**
 * Playwright global setup. Runs once before all tests.
 *
 * Responsibilities:
 *  1. Verify the native Go engine is ready (no external CLI dependencies).
 *  2. Create a local bare git repo and expose its file:// URL.
 *  3. Start a mock LLM provider HTTP server and capture its port.
 *  4. Spawn the Go server with the right env vars (E2E_MODE + the URLs above).
 *  5. Wait for the Go server, then wipe the e2e database so every run starts clean.
 */
export default async function globalSetup(config: FullConfig): Promise<void> {
    const baseURL = config.projects[0]?.use?.baseURL || 'http://localhost:8080';

    // 1. Create isolated home directory for E2E tests
    const e2eHome = createE2EHome();
    console.log(`[globalSetup] E2E home: ${e2eHome}`);

    // 3. Local bare git repo
    const repoUrl = setupBareRepo();
    console.log(`[globalSetup] bare repo URL: ${repoUrl}`);

    // 4. Mock LLM provider server
    const mock = await startMockProviderServer();
    console.log(`[globalSetup] mock provider: ${mock.baseUrl}`);

    // 4b. Mock deploy-target server (Vercel + GitHub API emulation).
    const mockDeploy = await startMockDeployServer();
    console.log(`[globalSetup] mock deploy target: ${mockDeploy.baseUrl}`);

    // 5. Persist env for tests
    const envData = {
        E2E_MOCK_PROVIDER_URL: mock.baseUrl,
        E2E_TEST_REPO_URL: repoUrl,
        E2E_HEADCOUNT1_HOME: e2eHome,
        E2E_MOCK_DEPLOY_URL: mockDeploy.baseUrl,
    };
    fs.writeFileSync(envFile, JSON.stringify(envData, null, 2));
    process.env.E2E_MOCK_PROVIDER_URL = mock.baseUrl;
    process.env.E2E_TEST_REPO_URL = repoUrl;
    process.env.E2E_MODE = 'true';

    // 6. Spawn the Go server with the right env (native engine by default).
    const env: Record<string, string> = { ...process.env as Record<string, string> };
    Object.assign(env, envData);
    env.E2E_MODE = 'true';
    env.E2E_HEADCOUNT1_HOME = e2eHome;
    // Point the deploysync clients at the mock deploy-target server.
    env.VERCEL_API_URL = mockDeploy.baseUrl;
    env.GITHUB_API_URL = mockDeploy.baseUrl;

    const projectRoot = path.resolve(__dirname, '..');
    // CI prebuilds the server binary (see .github/workflows/e2e.yml) so module
    // download + compilation happen in their own step instead of racing the
    // 60s server-ready timeout below on a cold module cache. Local runs (no
    // prebuilt binary) fall back to `go run .`.
    const prebuiltBinary = path.join(projectRoot, 'agent-orchestrator');
    const usePrebuilt = fs.existsSync(prebuiltBinary);
    console.log(`[globalSetup] starting server via ${usePrebuilt ? prebuiltBinary : 'go run .'}`);
    serverProcess = spawn(usePrebuilt ? prebuiltBinary : 'go', usePrebuilt ? [] : ['run', '.'], {
        cwd: projectRoot,
        env,
        stdio: ['ignore', 'pipe', 'pipe'],
    });

    serverProcess.stdout?.on('data', (data) => {
        process.stdout.write(`[server] ${data}`);
    });
    serverProcess.stderr?.on('data', (data) => {
        process.stderr.write(`[server] ${data}`);
    });
    serverProcess.on('exit', (code) => {
        console.log(`[globalSetup] Go server exited with code ${code}`);
    });

    // Persist the PID so teardown can kill it
    if (serverProcess.pid) {
        fs.writeFileSync(pidFile, String(serverProcess.pid));
    }

    // 6. Wait for the Go server, then wipe the DB
    await waitForServer(baseURL);
    const wipeRes = await fetch(`${baseURL}/api/e2e/wipe-db`, { method: 'POST' });
    if (!wipeRes.ok) {
        const text = await wipeRes.text();
        throw new Error(
            `globalSetup: wipe-db failed (${wipeRes.status}): ${text}. ` +
            `Ensure the Go server is running with E2E_MODE=true.`,
        );
    }
    console.log(`[globalSetup] wiped database via /api/e2e/wipe-db`);
}

// 120s: generous enough to cover a `go run .` cold-compile fallback (no
// prebuilt binary) on a slow machine, while the CI-prebuilt-binary path
// above starts in well under a second.
async function waitForServer(url: string, timeoutMs = 120_000): Promise<void> {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        try {
            const res = await fetch(`${url}/api/ping`);
            if (res.ok) return;
        } catch {
            /* keep trying */
        }
        await new Promise((r) => setTimeout(r, 500));
    }
    throw new Error(`globalSetup: server at ${url} did not become reachable within ${timeoutMs}ms`);
}
