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
    // Stop the mock Hindsight server if global-setup started one in this process.
    const stopHindsight = (globalThis as any).__e2eMockHindsightStop;
    if (typeof stopHindsight === 'function') {
        try {
            await stopHindsight();
            console.log('[globalTeardown] stopped mock hindsight server');
        } catch { /* ignore */ }
    }

    if (!fs.existsSync(pidFile)) {
        console.log('[globalTeardown] no pid file found, nothing to stop');
        return;
    }
    const pid = parseInt(fs.readFileSync(pidFile, 'utf8').trim(), 10);
    if (!pid) {
        console.log('[globalTeardown] invalid pid, nothing to stop');
        return;
    }
    try {
        process.kill(pid, 'SIGTERM');
        // Wait a bit, then force-kill
        setTimeout(() => {
            try { process.kill(pid, 'SIGKILL'); } catch { /* already dead */ }
        }, 2000);
        console.log(`[globalTeardown] sent SIGTERM to pid ${pid}`);
    } catch (err: any) {
        console.log(`[globalTeardown] failed to kill pid ${pid}: ${err.message}`);
    } finally {
        try { fs.unlinkSync(pidFile); } catch { /* ignore */ }
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
