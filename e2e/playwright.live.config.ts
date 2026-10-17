import { defineConfig, devices } from '@playwright/test';
import * as fs from 'fs';
import { loadLiveEnvFile } from './live/helpers/live-env';

// The live suite: real models do real tasks through the UI. It is opt-in
// (`make e2e-live`), never part of `make e2e` or the PR workflow, because it
// costs money and takes minutes per task. Without credentials every spec skips.
loadLiveEnvFile();

const PREINSTALLED_CHROMIUM = '/opt/pw-browsers/chromium';
const SYSTEM_CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const executablePath = fs.existsSync(PREINSTALLED_CHROMIUM)
    ? PREINSTALLED_CHROMIUM
    : fs.existsSync(SYSTEM_CHROME) ? SYSTEM_CHROME : undefined;

export default defineConfig({
    // One spec is one real task; the slowest (multi-agent) gets its own budget.
    timeout: 60 * 60 * 1000,
    globalTimeout: 3 * 60 * 60 * 1000,
    testDir: './live',
    testMatch: /.*\.spec\.ts/,
    fullyParallel: false,
    forbidOnly: !!process.env.CI,
    // A rerun of a real task is a second bill and rarely a different answer.
    retries: 0,
    workers: 1,
    reporter: process.env.CI
        ? [['line'], ['html', { outputFolder: process.env.PLAYWRIGHT_REPORT_DIR || 'playwright-report-live', open: 'never' }]]
        : [['list'], ['html', { outputFolder: 'playwright-report-live', open: 'never' }]],
    outputDir: 'test-results-live',
    use: {
        baseURL: process.env.E2E_BASE_URL || 'http://localhost:8080',
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        video: 'off',
        ...(executablePath ? { launchOptions: { executablePath } } : {}),
    },
    globalSetup: require.resolve('./global-setup.ts'),
    globalTeardown: require.resolve('./global-teardown.ts'),
    projects: [
        {
            name: 'live',
            use: { ...devices['Desktop Chrome'] },
            grepInvert: process.env.E2E_LIVE_FULL ? undefined : /@full/,
        },
    ],
});
