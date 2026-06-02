import * as fs from 'fs';
import * as path from 'path';
import { execSync } from 'child_process';

const pidFile = process.env.E2E_PID_FILE || path.join(__dirname, '.e2e-server.pid');

/**
 * Playwright global teardown. Kills the Go server child process started by
 * global-setup so the test run doesn't leave it running.
 */
export default async function globalTeardown(): Promise<void> {
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
}
