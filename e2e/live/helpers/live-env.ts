import { test } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../../helpers/env';

/** Where a contributor keeps the live credentials. Gitignored. */
export const LIVE_ENV_FILE = process.env.E2E_LIVE_ENV_FILE || path.join(__dirname, '..', '..', '.env.live');

/**
 * Reads KEY=VALUE lines from `.env.live` into process.env. A variable already
 * set in the environment (CI secrets) wins over the file.
 */
export function loadLiveEnvFile(file = LIVE_ENV_FILE): void {
    if (!fs.existsSync(file)) return;
    for (const raw of fs.readFileSync(file, 'utf8').split('\n')) {
        const line = raw.trim();
        if (!line || line.startsWith('#')) continue;
        const eq = line.indexOf('=');
        if (eq < 0) continue;
        const key = line.slice(0, eq).trim();
        let value = line.slice(eq + 1).trim();
        if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) value = value.slice(1, -1);
        if (process.env[key] === undefined) process.env[key] = value;
    }
}

export interface LiveEnv {
    baseUrl: string;
    apiKey: string;
    smartModel: string;
    cheapModel: string;
    /** Optional System One classifier (TypeSafe Jev). */
    typesafeKey: string;
    classifierModel: string;
    /** How long one small task may take to reach review. */
    taskTimeoutMs: number;
}

export function liveEnv(): LiveEnv | null {
    loadLiveEnvFile();
    let baseUrl = process.env.E2E_LIVE_BASE_URL || '';
    // `mock` runs the suite against the local mock provider: a check of the
    // harness itself (onboarding, the forms, the waits), not of the agents.
    if (baseUrl === 'mock') baseUrl = loadE2EEnv().E2E_MOCK_PROVIDER_URL;
    const apiKey = process.env.E2E_LIVE_API_KEY || '';
    const smartModel = process.env.E2E_LIVE_SMART_MODEL || '';
    if (!baseUrl || !apiKey || !smartModel) return null;
    return {
        baseUrl,
        apiKey,
        smartModel,
        cheapModel: process.env.E2E_LIVE_CHEAP_MODEL || smartModel,
        typesafeKey: process.env.E2E_LIVE_TYPESAFE_KEY || '',
        classifierModel: process.env.E2E_LIVE_CLASSIFIER_MODEL || 'jev-1.13',
        taskTimeoutMs: Number(process.env.E2E_LIVE_TASK_TIMEOUT_MIN || 20) * 60_000,
    };
}

/** Skips the calling spec when no live credentials are configured. */
export function requireLiveEnv(): LiveEnv {
    const env = liveEnv();
    test.skip(!env, `live e2e needs E2E_LIVE_BASE_URL, E2E_LIVE_API_KEY and E2E_LIVE_SMART_MODEL (env or ${LIVE_ENV_FILE})`);
    return env as LiveEnv;
}
