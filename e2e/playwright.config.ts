import { defineConfig, devices } from '@playwright/test';
import * as fs from 'fs';

// In the remote execution environment, chromium is pre-installed at a fixed symlink.
// Use it when present so the correct binary is used regardless of the Playwright version.
const PREINSTALLED_CHROMIUM = '/opt/pw-browsers/chromium';
const executablePath = fs.existsSync(PREINSTALLED_CHROMIUM) ? PREINSTALLED_CHROMIUM : undefined;

export default defineConfig({
    timeout: 120_000,
    testDir: './tests',
    fullyParallel: false,
    forbidOnly: !!process.env.CI,
    retries: process.env.CI ? 2 : 0,
    workers: 1,
    reporter: 'html',
    use: {
        baseURL: 'http://localhost:8080',
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
