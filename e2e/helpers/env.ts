import * as fs from 'fs';
import * as path from 'path';

const envFile = process.env.E2E_ENV_FILE || path.join(__dirname, '..', '.e2e-env.json');

interface E2EEnv {
    E2E_MOCK_PROVIDER_URL: string;
    E2E_TEST_REPO_URL: string;
    E2E_PAPERCLIP_HOME: string;
    /** Base URL of the mock Hindsight memory server (also the Go server's HINDSIGHT_API_URL). */
    E2E_HINDSIGHT_URL: string;
}

let cached: E2EEnv | null = null;

/**
 * Loads the e2e env written by `global-setup.ts`. Cached after the first call.
 * Throws if the env file is missing or malformed.
 */
export function loadE2EEnv(): E2EEnv {
    if (cached) return cached;
    if (!fs.existsSync(envFile)) {
        throw new Error(
            `loadE2EEnv: env file not found at ${envFile}. ` +
            `globalSetup must run before tests; check playwright.config.ts globalSetup.`,
        );
    }
    const data = JSON.parse(fs.readFileSync(envFile, 'utf8'));
    if (!data.E2E_MOCK_PROVIDER_URL || !data.E2E_TEST_REPO_URL || !data.E2E_PAPERCLIP_HOME) {
        throw new Error(
            `loadE2EEnv: env file at ${envFile} is missing E2E_MOCK_PROVIDER_URL, E2E_TEST_REPO_URL, or E2E_PAPERCLIP_HOME. ` +
            `Got: ${JSON.stringify(data)}`,
        );
    }
    cached = { E2E_MOCK_PROVIDER_URL: data.E2E_MOCK_PROVIDER_URL, E2E_TEST_REPO_URL: data.E2E_TEST_REPO_URL, E2E_PAPERCLIP_HOME: data.E2E_PAPERCLIP_HOME, E2E_HINDSIGHT_URL: data.E2E_HINDSIGHT_URL };
    return cached;
}
