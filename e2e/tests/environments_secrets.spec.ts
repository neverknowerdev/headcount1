import { test, expect, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';

const env = loadE2EEnv();

// Values the agent must be able to USE but never SEE. Long + unique so an
// accidental appearance anywhere is unambiguous.
const DEFAULT_SECRET_VALUE = 'e2e-default-secret-4f9a2b7c1d';
const STAGING_SECRET_VALUE = 'e2e-staging-secret-8e3c6d9a5f';

/**
 * Environments + secrets, end-to-end through the real engine:
 *
 *  - every company gets the three builtin environments, "headcount1 cloud"
 *    is the default
 *  - secrets are write-only: the API returns names + has_value, never values
 *  - a task's run receives its environment's secrets as shell env vars: the
 *    scripted agent proves the value is USABLE ([ "$VAR" = value ] → MATCH)
 *  - but never VISIBLE: echoing it back is redacted to [REDACTED:...] in the
 *    run log, the JSONL trajectory file, and the requests the LLM provider
 *    actually received
 *  - a task pinned to another environment gets that environment's secrets
 *    and not the default's
 */
test.describe.serial('Environments and secrets', () => {
    let companyId: number;
    let sprintId: number;
    let agentId: number;
    let defaultEnvId: number;
    let stagingEnvId: number;

    const headcount1Base = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');

    const cleanFilesystem = () => {
        for (const root of ['repos', 'workspace', 'artifacts', 'logs', 'skills']) {
            const fullPath = path.join(headcount1Base, root, 'env-co');
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }
    };

    test.beforeAll(async ({ request }) => {
        cleanFilesystem();
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });

        const provider = await postJSON(request, '/api/providers', {
            name: 'e2e-mock',
            base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key',
            provider_type: 'openai',
            default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model',
        });
        const company = await postJSON(request, '/api/companies', {
            name: 'Env Co', short_name: 'env-co', color: '#0ea5e9',
        });
        companyId = company.id;
        const agent = await postJSON(request, '/api/agents', {
            company_id: companyId,
            name: 'EnvRunner',
            system_prompt: 'You run shell commands.',
            model: 'e2e-mock-model',
            provider_id: provider.id,
        });
        agentId = agent.id;
        const sprint = await postJSON(request, '/api/sprints', {
            company_id: companyId, name: 'Env Sprint',
        });
        sprintId = sprint.id;
    });

    test.afterAll(async ({ request }) => {
        cleanFilesystem();
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
    });

    test('builtin environments are seeded; secrets are write-only', async ({ request }) => {
        const envs = await (await request.get(`/api/companies/${companyId}/environments`)).json();
        expect((envs as any[]).map((e) => e.name).sort()).toEqual(['headcount1 cloud', 'preview', 'production']);
        const def = (envs as any[]).find((e) => e.is_default);
        expect(def.name).toBe('headcount1 cloud');
        expect(def.builtin).toBeTruthy();
        defaultEnvId = def.id;

        // Add a secret to the default environment.
        const putRes = await request.put(`/api/environments/${defaultEnvId}/secrets`, {
            data: { name: 'DEFAULT_TOKEN', value: DEFAULT_SECRET_VALUE },
        });
        expect(putRes.ok()).toBeTruthy();
        const putBody = await putRes.text();
        expect(putBody).not.toContain(DEFAULT_SECRET_VALUE);
        expect(putBody).toContain('"has_value":true');

        // Create a user environment with its own secret.
        const staging = await postJSON(request, `/api/companies/${companyId}/environments`, { name: 'staging' });
        stagingEnvId = staging.id;
        await request.put(`/api/environments/${stagingEnvId}/secrets`, {
            data: { name: 'STAGING_TOKEN', value: STAGING_SECRET_VALUE },
        });

        // Listing returns names only — never a value, in any environment.
        const listing = await (await request.get(`/api/companies/${companyId}/environments`)).text();
        expect(listing).toContain('DEFAULT_TOKEN');
        expect(listing).toContain('STAGING_TOKEN');
        expect(listing).not.toContain(DEFAULT_SECRET_VALUE);
        expect(listing).not.toContain(STAGING_SECRET_VALUE);

        // Builtins cannot be renamed or deleted.
        expect((await request.put(`/api/environments/${defaultEnvId}`, { data: { name: 'x' } })).status()).toBe(400);
        expect((await request.delete(`/api/environments/${defaultEnvId}`)).status()).toBe(400);
    });

    test('agent can USE the default env secret but never SEE it', async ({ request }) => {
        const scenario = {
            entries: [
                // Prove the value is present and correct in the shell, then try
                // to leak it every obvious way.
                // The output markers are built by shell concatenation ("USE_""OK")
                // so the literal marker string never appears in the command text
                // itself — the run log records the command too, and the
                // assertions below must only match actual shell OUTPUT.
                { tool_call: { id: 'b1', name: 'bash', arguments: {
                    command: `[ "$DEFAULT_TOKEN" = "${DEFAULT_SECRET_VALUE}" ] && echo "USE_""OK" || echo "USE_""FAIL"; echo "leak-attempt: $DEFAULT_TOKEN"; env | grep DEFAULT_TOKEN || true`,
                } } },
                { tool_call: { id: 'f1', name: 'finish_task', arguments: {
                    task_status: 'done',
                    finish_status: 'Checked the environment secret.',
                } } },
            ],
        };
        await setScenario(scenario);

        const task = await postJSON(request, '/api/tasks', {
            company_id: companyId,
            sprint_id: sprintId,
            agent_id: agentId,
            title: 'Use default env secret',
            description: 'Run the env check.',
            task_type: 'implement',
            // Coder config has the shell tool; the default (CEO) is delegation-only.
            agent_config_name: 'Coder',
        });
        await request.put(`/api/tasks/${task.id}`, { data: { status: 'to-do' } });
        await waitForTaskStatus(request, task.id, 'done', 90_000);

        const runs = await (await request.get(`/api/tasks/${task.id}/runs`)).json();
        expect(runs.length).toBe(1);
        const run = await (await request.get(`/api/runs/${runs[0].id}`)).json();
        const logText = JSON.stringify(run.log_entries);

        // USABLE: the shell saw the real value (marker only exists as output).
        expect(logText).toContain('USE_OK');
        expect(logText).not.toContain('USE_FAIL');
        // NOT VISIBLE: neither echo nor env dump leaked it — redacted instead.
        expect(logText).not.toContain(DEFAULT_SECRET_VALUE);
        expect(logText).toContain('[REDACTED:');

        // The JSONL trajectory file (full fidelity) is clean too.
        const runDir = path.join(headcount1Base, 'logs', 'env-co', String(task.id), `run-${runs[0].id}`);
        const mainLog = fs.readFileSync(path.join(runDir, 'main.jsonl'), 'utf8');
        expect(mainLog).toContain('USE_OK');
        expect(mainLog).not.toContain(DEFAULT_SECRET_VALUE);

        // Strongest guarantee: nothing the SERVER produces for the provider
        // carries the value — the system prompt announces the var NAME only,
        // and tool results are scrubbed before entering the conversation.
        // (Assistant messages are excluded: they are the provider's own
        // scripted output — this test deliberately embeds the value there —
        // and are echoed back verbatim as conversation history.)
        const mockLog = await (await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const serverAuthored: string[] = [];
        let sawSecretNameInSystemPrompt = false;
        for (const req of (mockLog.requests as any[])) {
            if (req.path !== '/v1/chat/completions') continue;
            for (const msg of ((req.body as any)?.messages ?? [])) {
                if (msg.role === 'assistant') continue;
                serverAuthored.push(JSON.stringify(msg));
                if (msg.role === 'system' && String(msg.content).includes('$DEFAULT_TOKEN')) {
                    sawSecretNameInSystemPrompt = true;
                }
            }
        }
        expect(sawSecretNameInSystemPrompt).toBeTruthy();
        for (const msg of serverAuthored) {
            expect(msg).not.toContain(DEFAULT_SECRET_VALUE);
        }
    });

    test('a task pinned to another environment gets that env, not the default', async ({ request }) => {
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
        const scenario = {
            entries: [
                { tool_call: { id: 'b2', name: 'bash', arguments: {
                    command: `[ "$STAGING_TOKEN" = "${STAGING_SECRET_VALUE}" ] && echo "STG_""OK" || echo "STG_""FAIL"; [ -z "$DEFAULT_TOKEN" ] && echo "DEF_""GONE" || echo "DEF_""HERE"`,
                } } },
                { tool_call: { id: 'f2', name: 'finish_task', arguments: {
                    task_status: 'done',
                    finish_status: 'Checked staging environment.',
                } } },
            ],
        };
        await setScenario(scenario);

        const task = await postJSON(request, '/api/tasks', {
            company_id: companyId,
            sprint_id: sprintId,
            agent_id: agentId,
            title: 'Use staging env secret',
            description: 'Run the staging env check.',
            task_type: 'implement',
            agent_config_name: 'Coder',
            environment_id: stagingEnvId,
        });
        expect((await (await request.get(`/api/tasks/${task.id}`)).json()).environment_id).toBe(stagingEnvId);

        await request.put(`/api/tasks/${task.id}`, { data: { status: 'to-do' } });
        await waitForTaskStatus(request, task.id, 'done', 90_000);

        const runs = await (await request.get(`/api/tasks/${task.id}/runs`)).json();
        const run = await (await request.get(`/api/runs/${runs[0].id}`)).json();
        const logText = JSON.stringify(run.log_entries);
        expect(logText).toContain('STG_OK');
        expect(logText).not.toContain('STG_FAIL');
        expect(logText).toContain('DEF_GONE');
        expect(logText).not.toContain(STAGING_SECRET_VALUE);
    });

    test('environments UI: manage envs and secrets, system group is separate', async ({ page }) => {
        await page.goto('/companies/env-co/environments');
        // Generous timeout: the first UI visit may sit behind the SetupGate
        // while the server finishes its one-time dependency install.
        await expect(page.getByRole('heading', { name: 'Environments' })).toBeVisible({ timeout: 90_000 });

        // The three builtins plus staging are shown; default is labeled.
        await expect(page.getByTestId('env-headcount1 cloud')).toBeVisible();
        await expect(page.getByTestId('env-preview')).toBeVisible();
        await expect(page.getByTestId('env-production')).toBeVisible();
        await expect(page.getByTestId('env-staging')).toBeVisible();
        await expect(page.getByTestId('env-headcount1 cloud').getByText('default', { exact: true })).toBeVisible();

        // System-managed credentials are grouped separately in the default env
        // (the mock provider's API key exists, so the group must render).
        const defaultCard = page.getByTestId('env-headcount1 cloud');
        await expect(defaultCard.getByText('Managed by headcount1')).toBeVisible();
        await expect(defaultCard.getByText('e2e-mock API key')).toBeVisible();

        // Existing secrets are listed by name, values masked.
        await expect(defaultCard.getByTestId('secret-DEFAULT_TOKEN')).toBeVisible();
        await expect(defaultCard.getByTestId('secret-DEFAULT_TOKEN')).toContainText('••••');

        // Add a secret through the UI.
        const stagingCard = page.getByTestId('env-staging');
        await stagingCard.getByPlaceholder('API_KEY').fill('UI_ADDED_TOKEN');
        await stagingCard.getByPlaceholder('secret value').fill('ui-added-value-123456');
        await stagingCard.getByRole('button', { name: 'Save' }).click();
        await expect(stagingCard.getByTestId('secret-UI_ADDED_TOKEN')).toBeVisible();

        // Create a new environment through the UI.
        await page.getByPlaceholder('New environment name (e.g. staging)').fill('qa');
        await page.getByRole('button', { name: 'Add environment' }).click();
        await expect(page.getByTestId('env-qa')).toBeVisible();

        // Builtin cards expose no rename/delete controls.
        await expect(defaultCard.getByTitle('Delete', { exact: true })).toHaveCount(0);
        await expect(defaultCard.getByTitle('Rename', { exact: true })).toHaveCount(0);
    });

    test('task modal offers the environment picker', async ({ page }) => {
        await page.goto('/companies/env-co/tasks');
        await page.getByRole('button', { name: /New Task|Add Task|Create/i }).first().click({ timeout: 90_000 });
        const envSelect = page.locator('select[title*="environment\'s secrets"]');
        await expect(envSelect).toBeVisible();
        const options = await envSelect.locator('option').allTextContents();
        expect(options.join(',')).toContain('headcount1 cloud (default)');
        expect(options.join(',')).toContain('staging');
    });
});

async function setScenario(scenario: unknown): Promise<void> {
    const res = await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(scenario),
    });
    if (!res.ok) throw new Error(`set-scenario failed: ${res.status}`);
}

async function postJSON(request: APIRequestContext, url: string, data: unknown): Promise<any> {
    const res = await request.post(url, { data });
    if (!res.ok()) {
        throw new Error(`POST ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}
