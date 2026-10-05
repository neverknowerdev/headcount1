import { defineConfig, devices } from '@playwright/test';
import * as fs from 'fs';

// In the remote execution environment, chromium is pre-installed at a fixed symlink.
// Use it when present so the correct binary is used regardless of the Playwright version.
const PREINSTALLED_CHROMIUM = '/opt/pw-browsers/chromium';
// CI supplies the pinned Playwright binary. Local macOS contributors can use
// an installed Chrome without downloading another browser just to run E2E.
const SYSTEM_CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const executablePath = fs.existsSync(PREINSTALLED_CHROMIUM)
    ? PREINSTALLED_CHROMIUM
    : fs.existsSync(SYSTEM_CHROME) ? SYSTEM_CHROME : undefined;

export default defineConfig({
    timeout: 150_000,
    globalTimeout: 20 * 60 * 1000,
    maxFailures: process.env.CI ? 1 : 0,
    testDir: './tests',
    fullyParallel: false,
    forbidOnly: !!process.env.CI,
    retries: process.env.CI ? 1 : 0,
    workers: 1,
    reporter: process.env.CI
        ? [['line'], ['html', { outputFolder: process.env.PLAYWRIGHT_REPORT_DIR || 'playwright-report', open: 'never' }]]
        : [['list'], ['html', { open: 'never' }]],
    use: {
        baseURL: process.env.E2E_BASE_URL || 'http://localhost:8080',
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        video: 'retain-on-failure',
        ...(executablePath ? { launchOptions: { executablePath } } : {}),
    },
    globalSetup: require.resolve('./global-setup.ts'),
    globalTeardown: require.resolve('./global-teardown.ts'),
    projects: [
        {
            name: 'chromium',
            use: { ...devices['Desktop Chrome'] },
        },
    ],
});
