import { test, expect, APIRequestContext } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';

const env = loadE2EEnv();

const SECRET_VALUE = 'deploy-secret-value-7a3f9c21';
const VARIABLE_VALUE = 'production';
const VERCEL_TOKEN = 'vercel-e2e-token-do-not-leak';
const GITHUB_TOKEN = 'github-e2e-token-do-not-leak';

interface MockRequest {
    method: string;
    path: string;
    body: any;
    auth: string;
}

async function deployRequests(): Promise<MockRequest[]> {
    const res = await fetch(`${env.E2E_MOCK_DEPLOY_URL}/__test/requests`);
    return (await res.json()).requests;
}

/**
 * Deploy env-var sync, end to end against a mock Vercel/GitHub API server:
 *
 *  - project environments are created under a project; entries have a kind
 *    (secret / variable), both sealed at rest, values never returned by the API
 *  - a Vercel connector pushes secrets as "sensitive" and variables as
 *    "plain" env vars; a GitHub connector pushes sealed environment secrets
 *    and plaintext environment variables
 *  - entry changes push automatically; deletes propagate; "Sync now" pushes
 *    everything and records per-connector status
 *  - connector tokens never appear in any API response
 */
test.describe.serial('Deploy environment sync', () => {
    let companyId: number;
    let projectId: number;
    let envId: number;

    test.beforeAll(async ({ request }) => {
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_DEPLOY_URL}/__test/reset`, { method: 'POST' });

        const company = await postJSON(request, '/api/companies', {
            name: 'Deploy Co', short_name: 'deploy-co', color: '#16a34a',
        });
        companyId = company.id;
        const project = await postJSON(request, '/api/projects', {
            company_id: companyId, name: 'Webshop',
        });
        projectId = project.id;
    });

    test.afterAll(async ({ request }) => {
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_DEPLOY_URL}/__test/reset`, { method: 'POST' });
    });

    test('project environments hold kinded entries, values write-only', async ({ request }) => {
        // Projects start with no environments.
        let res = await request.get(`/api/projects/${projectId}/environments`);
        expect(res.ok()).toBeTruthy();
        expect(await res.json()).toEqual([]);

        const created = await postJSON(request, `/api/projects/${projectId}/environments`, { name: 'production' });
        envId = created.id;

        await putJSON(request, `/api/environments/${envId}/secrets`, {
            name: 'API_KEY', value: SECRET_VALUE, kind: 'secret',
        });
        await putJSON(request, `/api/environments/${envId}/secrets`, {
            name: 'NODE_ENV', value: VARIABLE_VALUE, kind: 'variable',
        });

        res = await request.get(`/api/projects/${projectId}/environments`);
        const listing = await res.text();
        expect(listing).toContain('"API_KEY"');
        expect(listing).toContain('"kind":"secret"');
        expect(listing).toContain('"kind":"variable"');
        expect(listing).not.toContain(SECRET_VALUE);
    });

    test('connector discovery lists targets without storing the token', async ({ request }) => {
        const vercelProjects = await postJSON(request, '/api/connector-discovery', {
            provider: 'vercel', token: VERCEL_TOKEN,
        });
        expect(vercelProjects.projects.map((p: any) => p.id)).toContain('prj_mock1');

        const vercelEnvs = await postJSON(request, '/api/connector-discovery', {
            provider: 'vercel', token: VERCEL_TOKEN, project: 'prj_mock1',
        });
        expect(vercelEnvs.environments.map((e: any) => e.id)).toEqual(
            expect.arrayContaining(['production', 'preview', 'development', 'env_custom1']));

        const repos = await postJSON(request, '/api/connector-discovery', {
            provider: 'github', token: GITHUB_TOKEN,
        });
        expect(repos.projects.map((p: any) => p.id)).toContain('acme/web');

        const ghEnvs = await postJSON(request, '/api/connector-discovery', {
            provider: 'github', token: GITHUB_TOKEN, project: 'acme/web',
        });
        expect(ghEnvs.environments.map((e: any) => e.id)).toEqual(['production', 'staging']);
    });

    test('vercel connector pushes secrets as sensitive, variables as plain', async ({ request }) => {
        await fetch(`${env.E2E_MOCK_DEPLOY_URL}/__test/reset`, { method: 'POST' });

        const conn = await postJSON(request, `/api/environments/${envId}/connectors`, {
            provider: 'vercel', token: VERCEL_TOKEN,
            target_project: 'prj_mock1', target_environment: 'production',
        });
        expect(JSON.stringify(conn)).not.toContain(VERCEL_TOKEN);
        expect(conn.has_token).toBeTruthy();

        // Connector creation triggers an initial push (async) — poll for it.
        await expect.poll(async () => {
            const reqs = await deployRequests();
            return reqs.some((r) => r.method === 'POST' && r.path.startsWith('/v10/projects/prj_mock1/env'));
        }, { timeout: 15_000 }).toBeTruthy();

        const reqs = await deployRequests();
        const push = reqs.find((r) => r.method === 'POST' && r.path.startsWith('/v10/projects/prj_mock1/env'));
        expect(push!.path).toContain('upsert=true');
        expect(push!.auth).toBe(`Bearer ${VERCEL_TOKEN}`);
        const items: any[] = push!.body;
        const byKey = Object.fromEntries(items.map((i) => [i.key, i]));
        expect(byKey.API_KEY.type).toBe('sensitive');
        expect(byKey.API_KEY.value).toBe(SECRET_VALUE);
        expect(byKey.API_KEY.target).toEqual(['production']);
        expect(byKey.NODE_ENV.type).toBe('plain');
        expect(byKey.NODE_ENV.value).toBe(VARIABLE_VALUE);
    });

    test('github connector pushes sealed secrets and plain variables', async ({ request }) => {
        await fetch(`${env.E2E_MOCK_DEPLOY_URL}/__test/reset`, { method: 'POST' });

        await postJSON(request, `/api/environments/${envId}/connectors`, {
            provider: 'github', token: GITHUB_TOKEN,
            target_project: 'acme/web', target_environment: 'production',
        });

        await expect.poll(async () => {
            const reqs = await deployRequests();
            return reqs.some((r) => r.method === 'PUT' && r.path === '/repos/acme/web/environments/production/secrets/API_KEY');
        }, { timeout: 15_000 }).toBeTruthy();

        const reqs = await deployRequests();
        const secretPut = reqs.find((r) => r.method === 'PUT' && r.path.includes('/secrets/API_KEY'))!;
        expect(secretPut.auth).toBe(`Bearer ${GITHUB_TOKEN}`);
        expect(secretPut.body.key_id).toBe('mock-key-1');
        // Sealed client-side: the plaintext must NOT cross the wire.
        expect(secretPut.body.encrypted_value).toBeTruthy();
        expect(JSON.stringify(secretPut.body)).not.toContain(SECRET_VALUE);

        const varPost = reqs.find((r) => r.method === 'POST' && r.path.includes('/variables'))!;
        expect(varPost.body).toEqual({ name: 'NODE_ENV', value: VARIABLE_VALUE });
    });

    test('entry changes and deletes propagate; sync reports status', async ({ request }) => {
        await fetch(`${env.E2E_MOCK_DEPLOY_URL}/__test/reset`, { method: 'POST' });

        // Changing a value pushes to BOTH connectors automatically.
        await putJSON(request, `/api/environments/${envId}/secrets`, {
            name: 'API_KEY', value: SECRET_VALUE + '-rotated', kind: 'secret',
        });
        await expect.poll(async () => {
            const reqs = await deployRequests();
            const vercelPush = reqs.some((r) => r.method === 'POST' && r.path.startsWith('/v10/projects/'));
            const githubPush = reqs.some((r) => r.method === 'PUT' && r.path.includes('/secrets/API_KEY'));
            return vercelPush && githubPush;
        }, { timeout: 15_000 }).toBeTruthy();

        // Manual sync returns per-connector status.
        const syncRes = await request.post(`/api/environments/${envId}/sync`);
        expect(syncRes.ok()).toBeTruthy();
        const connectors = await syncRes.json();
        expect(connectors.length).toBe(2);
        for (const c of connectors) {
            expect(c.last_sync_status).toBe('ok');
            expect(c.last_sync_at).toBeTruthy();
        }
        expect(JSON.stringify(connectors)).not.toContain(VERCEL_TOKEN);
        expect(JSON.stringify(connectors)).not.toContain(GITHUB_TOKEN);

        // Deleting an entry issues deletes on both targets.
        await fetch(`${env.E2E_MOCK_DEPLOY_URL}/__test/reset`, { method: 'POST' });
        const del = await request.delete(`/api/environments/${envId}/secrets/NODE_ENV`);
        expect(del.status()).toBe(204);
        await expect.poll(async () => {
            const reqs = await deployRequests();
            const gh = reqs.some((r) => r.method === 'DELETE' && r.path.includes('/variables/NODE_ENV'));
            const vercelList = reqs.some((r) => r.method === 'GET' && r.path.startsWith('/v10/projects/prj_mock1/env'));
            return gh && vercelList;
        }, { timeout: 15_000 }).toBeTruthy();
    });

    test('project settings UI manages environments and connectors', async ({ page, request }) => {
        await page.goto(`/companies/deploy-co/projects/${projectId}`);
        const section = page.getByTestId('project-environments');
        await expect(section).toBeVisible({ timeout: 90_000 });

        // The production env with its entries and both connectors is shown.
        const envCard = section.getByTestId('project-env-production');
        await expect(envCard).toBeVisible();
        await expect(envCard.getByTestId('entry-API_KEY')).toBeVisible();
        await expect(envCard.getByTestId('connector-vercel-prj_mock1')).toBeVisible();
        await expect(envCard.getByTestId('connector-github-acme/web')).toBeVisible();
        await expect(envCard.getByText('synced').first()).toBeVisible();

        // Add a variable through the UI.
        await envCard.locator('select').first().selectOption('variable');
        await envCard.getByPlaceholder('API_KEY').fill('REGION');
        await envCard.getByPlaceholder('value').fill('eu-west-1');
        await envCard.getByRole('button', { name: 'Save' }).click();
        await expect(envCard.getByTestId('entry-REGION')).toBeVisible();

        // Connector wizard: token → projects → environments (from the mock).
        await envCard.getByRole('button', { name: 'Connect', exact: true }).click();
        const wizard = page.getByTestId('connector-wizard');
        await expect(wizard).toBeVisible();
        await wizard.getByPlaceholder('token').fill(VERCEL_TOKEN);
        await wizard.getByRole('button', { name: 'List projects' }).click();
        await wizard.locator('select').nth(1).selectOption({ label: 'mock-api' });
        await expect(wizard.locator('select').nth(2).locator('option', { hasText: 'preview' })).toBeAttached({ timeout: 10_000 });
        await wizard.locator('select').nth(2).selectOption('preview');
        await wizard.getByRole('button', { name: 'Connect', exact: true }).click();
        await expect(envCard.getByTestId('connector-vercel-prj_mock2')).toBeVisible();

        // A second environment can be added through the UI.
        await section.getByPlaceholder('New environment (e.g. production)').fill('staging');
        await section.getByRole('button', { name: 'Add environment' }).click();
        await expect(section.getByTestId('project-env-staging')).toBeVisible();

        // Nothing anywhere in the page carries a token or secret value.
        const html = await page.content();
        expect(html).not.toContain(VERCEL_TOKEN);
        expect(html).not.toContain(GITHUB_TOKEN);
        expect(html).not.toContain(SECRET_VALUE);

        // And the platform env page still exists for run-injected secrets.
        const companyEnvs = await (await request.get(`/api/companies/${companyId}/environments`)).json();
        expect(companyEnvs.length).toBe(1);
        expect(companyEnvs[0].name).toBe('headcount1 cloud');
    });
});

async function postJSON(request: APIRequestContext, url: string, data: unknown): Promise<any> {
    const res = await request.post(url, { data });
    if (!res.ok()) {
        throw new Error(`POST ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}

async function putJSON(request: APIRequestContext, url: string, data: unknown): Promise<any> {
    const res = await request.put(url, { data });
    if (!res.ok()) {
        throw new Error(`PUT ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}
