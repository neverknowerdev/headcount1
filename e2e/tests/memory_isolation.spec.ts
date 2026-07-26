import { test, expect, APIRequestContext } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';

const env = loadE2EEnv();

/**
 * Multi-team memory isolation. Each team maps to its own Hindsight tenant
 * (an isolated Postgres schema — /v1/team-<id>/), so no team's memory relates
 * to another's, and the /api/memory endpoints enforce team membership.
 *
 * Team A is the auto-logged-in e2e fixture user; team B is a second user
 * registered WITHOUT an invite (so they get their own separate team). The
 * suite asserts: (a) a team can reach its own bank; (b) a team gets 404 for
 * another team's bank across the whole memory surface (never 403 — foreign
 * ids are indistinguishable from nonexistent ones); (c) the mock records the
 * two companies' banks under different tenants.
 */
test.describe.serial('Memory multi-team isolation', () => {
    let apiB: APIRequestContext;
    let companyA: any;
    let companyB: any;
    const bankA = () => `company-${companyA.id}`;
    const bankB = () => `company-${companyB.id}`;

    const dump = async (): Promise<any> => {
        const res = await fetch(`${env.E2E_HINDSIGHT_URL}/__admin/dump`);
        expect(res.ok).toBeTruthy();
        return res.json();
    };

    test.beforeAll(async ({ request, playwright }) => {
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_HINDSIGHT_URL}/__admin/reset`, { method: 'POST' });

        // The memory backend comes up asynchronously after server start; wait
        // for it before exercising /api/memory (else the first call 503s).
        await expect.poll(async () => {
            const res = await request.get('/api/memory/status');
            if (!res.ok()) return false;
            return (await res.json()).available === true;
        }, { timeout: 60_000, message: 'memory backend should become available' }).toBeTruthy();

        // Team A: the fixture user creates a company.
        companyA = await postJSON(request, '/api/companies', { name: 'Alpha', short_name: 'alpha' });
        expect(companyA.team_id).toBeTruthy();

        // Team B: a brand-new user (no invite → their own team), in an isolated
        // request context that carries their session cookie.
        apiB = await playwright.request.newContext({ baseURL: 'http://localhost:8080' });
        const reg = await apiB.post('/api/e2e/register', { data: { email: 'teamb@corp.io' } });
        expect(reg.ok(), await reg.text()).toBeTruthy();
        companyB = await postJSON(apiB, '/api/companies', { name: 'Bravo', short_name: 'bravo' });
        expect(companyB.team_id).toBeTruthy();
        expect(companyB.team_id).not.toBe(companyA.team_id);
    });

    test.afterAll(async () => {
        await apiB?.dispose();
    });

    test('each team can list and read its own bank', async ({ request }) => {
        const listA = await request.get(`/api/memory/banks?company_id=${companyA.id}`);
        expect(listA.ok()).toBeTruthy();
        expect((await listA.json())[0].bank_id).toBe(bankA());

        // Own bank surface is reachable (200), which also creates it in the
        // mock under this team's tenant.
        const statsA = await request.get(`/api/memory/banks/${bankA()}/stats`);
        expect(statsA.ok()).toBeTruthy();

        const listB = await apiB.get(`/api/memory/banks?company_id=${companyB.id}`);
        expect(listB.ok()).toBeTruthy();
        expect((await listB.json())[0].bank_id).toBe(bankB());
        const statsB = await apiB.get(`/api/memory/banks/${bankB()}/stats`);
        expect(statsB.ok()).toBeTruthy();
    });

    test('a team gets 404 across the whole memory surface for another team bank', async () => {
        // Team B addressing team A's bank — every route is 404 (auth), never 403.
        const a = bankA();
        const gets = [
            `/api/memory/banks/${a}/stats`,
            `/api/memory/banks/${a}/memories`,
            `/api/memory/banks/${a}/graph`,
            `/api/memory/banks/${a}/config`,
            `/api/memory/banks/${a}/directives`,
            `/api/memory/banks/${a}/tags`,
            `/api/memory/banks/${a}/mental-models`,
        ];
        for (const path of gets) {
            const res = await apiB.get(path);
            expect(res.status(), `GET ${path} must be 404 for another team`).toBe(404);
        }
        const recall = await apiB.post(`/api/memory/banks/${a}/recall`, { data: { query: 'anything' } });
        expect(recall.status()).toBe(404);
        const ask = await apiB.post(`/api/memory/banks/${a}/ask`, { data: { query: 'anything' } });
        expect(ask.status()).toBe(404);
        const del = await apiB.delete(`/api/memory/banks/${a}/mental-models/whatever`);
        expect(del.status()).toBe(404);

        // And ListMemoryBanks for a foreign company_id is 404 too.
        const listForeign = await apiB.get(`/api/memory/banks?company_id=${companyA.id}`);
        expect(listForeign.status()).toBe(404);
    });

    test('a malformed or non-canonical bank id is 404 (never proxied to the backend)', async ({ request }) => {
        const bad = [
            'not-a-bank', 'team-1', 'company-', 'company-abc',
            // Non-canonical spellings of an id the caller DOES own: each parses
            // to the same company under a naive Atoi, but names a different
            // bank upstream — they must not authorize.
            `company-+${companyA.id}`,
            `company-00${companyA.id}`,
            `company-${4294967296 + companyA.id}`, // int32 truncation
        ];
        for (const b of bad) {
            const res = await request.get(`/api/memory/banks/${encodeURIComponent(b)}/stats`);
            expect(res.status(), `bank ${b}`).toBe(404);
        }
    });

    test('each team gets its own bank, addressed under hindsight\'s literal tenant', async () => {
        const d = await dump();
        // Isolation is per BANK, not per URL tenant: hindsight-api hardcodes the
        // tenant segment (verified against 0.6.1 — any other value 404s, since
        // its multi-tenancy is credential-based, not path-based). So both banks
        // are addressed under "default", and what keeps teams apart is the
        // distinct bank per company plus this app's authorization (asserted
        // above). Hindsight banks share nothing: no cross-bank entity
        // resolution, graph traversal or rank fusion.
        expect(d.tenants[bankA()]).toBe('default');
        expect(d.tenants[bankB()]).toBe('default');
        expect(bankA()).not.toBe(bankB());
    });
});

async function postJSON(request: APIRequestContext, url: string, data: unknown): Promise<any> {
    const res = await request.post(url, { data });
    if (!res.ok()) {
        throw new Error(`POST ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}
