import * as fs from 'fs';
import * as path from 'path';
import { execSync } from 'child_process';
import { cleanupE2EHome } from './fixtures/e2e-home';

const pidFile = process.env.E2E_PID_FILE || path.join(__dirname, '.e2e-server.pid');
const envFile = process.env.E2E_ENV_FILE || path.join(__dirname, '.e2e-env.json');

/**
 * Playwright global teardown. Kills the Go server child process started by
 * global-setup so the test run doesn't leave it running.
 */
export default async function globalTeardown(): Promise<void> {
    // Kill the Go server FIRST: it holds keep-alive connections to the mock
    // servers, so stopping the mocks while it still runs delays their close().
    await stopGoServer();

    // Then stop the mock Hindsight server if global-setup started one in this process.
    const stopHindsight = (globalThis as any).__e2eMockHindsightStop;
    if (typeof stopHindsight === 'function') {
        try {
            await stopHindsight();
            console.log('[globalTeardown] stopped mock hindsight server');
        } catch { /* ignore */ }
    }

    // Clean up E2E home directory
    try {
        if (fs.existsSync(envFile)) {
            const data = JSON.parse(fs.readFileSync(envFile, 'utf8'));
            if (data.E2E_PAPERCLIP_HOME) {
                cleanupE2EHome(data.E2E_PAPERCLIP_HOME);
                console.log(`[globalTeardown] cleaned up E2E home: ${data.E2E_PAPERCLIP_HOME}`);
            }
        }
    } catch (err: any) {
        console.log(`[globalTeardown] failed to clean up E2E home: ${err.message}`);
    }
}

/**
 * SIGTERM the Go server from the pid file, wait (bounded) for it to exit,
 * and SIGKILL it if it's still alive — all awaited, so the escalation can't
 * be lost to the process exiting before a dangling timer fires.
 */
async function stopGoServer(): Promise<void> {
    if (!fs.existsSync(pidFile)) {
        console.log('[globalTeardown] no pid file found, nothing to stop');
        return;
    }
    const pid = parseInt(fs.readFileSync(pidFile, 'utf8').trim(), 10);
    if (!pid) {
        console.log('[globalTeardown] invalid pid, nothing to stop');
        try { fs.unlinkSync(pidFile); } catch { /* ignore */ }
        return;
    }
    try {
        process.kill(pid, 'SIGTERM');
        console.log(`[globalTeardown] sent SIGTERM to pid ${pid}`);
        const deadline = Date.now() + 5000;
        while (isAlive(pid) && Date.now() < deadline) {
            await sleep(200);
        }
        if (isAlive(pid)) {
            try {
                process.kill(pid, 'SIGKILL');
                console.log(`[globalTeardown] pid ${pid} ignored SIGTERM, sent SIGKILL`);
            } catch { /* died in the meantime */ }
        }
    } catch (err: any) {
        console.log(`[globalTeardown] failed to kill pid ${pid}: ${err.message}`);
    } finally {
        try { fs.unlinkSync(pidFile); } catch { /* ignore */ }
    }
}

function isAlive(pid: number): boolean {
    try {
        process.kill(pid, 0); // signal 0 = liveness probe
        return true;
    } catch {
        return false;
    }
}

function sleep(ms: number): Promise<void> {
    return new Promise((r) => setTimeout(r, ms));
}
