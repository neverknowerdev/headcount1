import * as fs from 'fs';
import * as path from 'path';
import { fetchWithTimeout } from './helpers/http';
import { terminatePid } from './helpers/process';
import { cleanupE2EHome } from './fixtures/e2e-home';

const pidFile = process.env.E2E_PID_FILE || path.join(__dirname, '.e2e-server.pid');
const envFile = process.env.E2E_ENV_FILE || path.join(__dirname, '.e2e-env.json');

/** Always completes within a bounded interval, including setup/test failures. */
export default async function globalTeardown(): Promise<void> {
    let data: { E2E_MOCK_PROVIDER_URL?: string; E2E_HEADCOUNT1_HOME?: string; E2E_SERVER_LOG?: string; E2E_RUN_DIR?: string } = {};
    try {
        if (fs.existsSync(envFile)) data = JSON.parse(fs.readFileSync(envFile, 'utf8'));
    } catch (err) {
        console.log(`[globalTeardown] could not read env metadata: ${(err as Error).message}`);
    }

    if (data.E2E_MOCK_PROVIDER_URL) {
        try {
            await fetchWithTimeout(`${data.E2E_MOCK_PROVIDER_URL}/__test/shutdown`, { method: 'POST' }, 2_000);
        } catch (err) {
            console.log(`[globalTeardown] mock provider shutdown request failed: ${(err as Error).message}`);
        }
    }

    try {
        if (fs.existsSync(pidFile)) {
            const raw = JSON.parse(fs.readFileSync(pidFile, 'utf8')) as { pid?: number; group?: boolean };
            if (raw.pid) await terminatePid(raw.pid, raw.group !== false, 4_000);
        }
    } catch (err) {
        console.log(`[globalTeardown] server shutdown failed: ${(err as Error).message}`);
    }

    if (data.E2E_HEADCOUNT1_HOME) {
        try { cleanupE2EHome(data.E2E_HEADCOUNT1_HOME); } catch (err) {
            console.log(`[globalTeardown] failed to clean E2E home: ${(err as Error).message}`);
        }
    }
    if (data.E2E_SERVER_LOG && fs.existsSync(data.E2E_SERVER_LOG)) {
        const contents = fs.readFileSync(data.E2E_SERVER_LOG, 'utf8');
        if (contents) console.log(`[globalTeardown] server log tail:\n${contents.slice(-8_000)}`);
    }
    try { fs.unlinkSync(pidFile); } catch { /* already absent */ }
    try { fs.unlinkSync(envFile); } catch { /* already absent */ }
    if (data.E2E_RUN_DIR) {
        try { fs.rmSync(data.E2E_RUN_DIR, { recursive: true, force: true }); } catch { /* best effort */ }
    }
}
