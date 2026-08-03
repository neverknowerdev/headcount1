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
    timeout: 120_000,
    testDir: './tests',
    fullyParallel: false,
    forbidOnly: !!process.env.CI,
    retries: process.env.CI ? 2 : 0,
    workers: 1,
    reporter: 'html',
    use: {
        baseURL: process.env.E2E_BASE_URL || 'http://localhost:8080',
        trace: 'on-first-retry',
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
