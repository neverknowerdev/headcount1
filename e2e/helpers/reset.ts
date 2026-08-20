import { APIRequestContext } from '@playwright/test';
import { fetchJSON, requireFetchOK } from './http';

/** Reset server and mock-provider state, failing with actionable diagnostics. */
export async function resetE2E(
    request: APIRequestContext,
    mockProviderUrl?: string,
): Promise<void> {
    const response = await request.post('/api/e2e/wipe-db', { timeout: 65_000 });
    if (!response.ok()) throw new Error(`POST /api/e2e/wipe-db failed (${response.status()}): ${await response.text()}`);
    if (mockProviderUrl) await requireFetchOK(`${mockProviderUrl}/__test/reset`, { method: 'POST' }, 5_000);
}

/** Same reset contract for isolated-server specs that do not use Playwright's request fixture. */
export async function resetE2EBase(baseUrl: string, mockProviderUrl?: string): Promise<void> {
    await requireFetchOK(`${baseUrl}/api/e2e/wipe-db`, { method: 'POST' }, 65_000);
    if (mockProviderUrl) await requireFetchOK(`${mockProviderUrl}/__test/reset`, { method: 'POST' }, 5_000);
}

export async function postMockJSON<T>(url: string, data: unknown): Promise<T> {
    return fetchJSON<T>(url, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(data),
    }, 5_000);
}
