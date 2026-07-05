import { test, expect, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';

const env = loadE2EEnv();

/**
 * Hindsight memory layer, end-to-end against the mock Hindsight server:
 *
 *   a. project doc ingestion (.md files of the git repo → bank proj-<id>)
 *   b. doc change tracking via POST /api/settings/sync (document upsert)
 *   c. agent experience: CTO fails a task, its error memory surfaces in a
 *      later CMO run via memory_recall; reruns then surface the history and
 *      add success memories (bank runs-<companyID>, tags agent:*)
 *   d. the /api/memory proxy surface (list, patch, delete, recall, ask)
 */
test.describe.serial('Memory (Hindsight) layer', () => {
    const shortName = 'memco';
    const projectName = 'GM Coin';
    const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');

    let companyId: number;
    let projectId: number;
    let providerId: number;
    let agentId: number;
    let sprintId: number;
    let task1Id: number; // CTO task
    let task2Id: number; // CMO task
    let projBank: string;
    let runsBank: string;

    const cleanFilesystem = () => {
        for (const subDir of [`data/${shortName}`, `companies/${shortName}`, `workspace/${shortName}`, `data/runs/${shortName}`, `data/artifacts/${shortName}`]) {
            const fullPath = path.join(paperclipBase, subDir);
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }
        if (providerId) {
            const providerFile = path.join(paperclipBase, 'data', 'llm-providers', `${providerId}.json`);
            if (fs.existsSync(providerFile)) fs.rmSync(providerFile, { force: true });
        }
    };

    const dumpBanks = async (): Promise<Record<string, any[]>> => {
        const res = await fetch(`${env.E2E_HINDSIGHT_URL}/__admin/dump`);
        expect(res.ok).toBeTruthy();
        return (await res.json()).banks as Record<string, any[]>;
    };

    const setScenario = async (entries: any[]) => {
        const res = await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ entries }),
        });
        expect(res.ok).toBeTruthy();
    };

    const resetProviderMock = async () => {
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
    };

    /** All tool-result message contents seen by the mock LLM since the last reset. */
    const toolResults = async (): Promise<string[]> => {
        const log = await (await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const out: string[] = [];
        for (const req of log.requests as any[]) {
            for (const msg of ((req.body as any)?.messages ?? [])) {
                if (msg.role === 'tool' && typeof msg.content === 'string') out.push(msg.content);
            }
        }
        return out;
    };

    test.beforeAll(async ({ request }) => {
        cleanFilesystem();
        await request.post('/api/e2e/wipe-db');
        await resetProviderMock();
        await fetch(`${env.E2E_HINDSIGHT_URL}/__admin/reset`, { method: 'POST' });
    });

    test.afterAll(async ({ request }) => {
        cleanFilesystem();
        await request.post('/api/e2e/wipe-db');
        await resetProviderMock();
        await fetch(`${env.E2E_HINDSIGHT_URL}/__admin/reset`, { method: 'POST' });
    });

    test('memory backend is reported available', async ({ request }) => {
        await expect.poll(async () => {
            const res = await request.get('/api/memory/status');
            if (!res.ok()) return false;
            return (await res.json()).available === true;
        }, { timeout: 60_000, message: 'memory backend should become available' }).toBeTruthy();
    });

    test('project docs are ingested into the project memory bank', async ({ request }) => {
        const company = await postJSON(request, '/api/companies', {
            name: 'Memory Co', short_name: shortName, color: '#0ea5e9',
        });
        companyId = company.id;
        runsBank = `runs-${companyId}`;

        const provider = await postJSON(request, '/api/providers', {
            name: 'memco-mock',
            base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key',
            provider_type: 'openai',
            default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model',
        });
        providerId = provider.id;

        const agent = await postJSON(request, '/api/agents', {
            company_id: companyId,
            name: 'MemAgent',
            system_prompt: 'You work on tasks.',
            model: 'e2e-mock-model',
            provider_id: providerId,
        });
        agentId = agent.id;

        // Creating a project with a git repo clones it and kicks off the
        // background doc-ingestion goroutine (polls memory availability at
        // ~10s intervals, so allow a generous window).
        const project = await postJSON(request, '/api/projects', {
            company_id: companyId,
            name: projectName,
            workspace_folder: `${shortName}/gm-coin`,
            repository_url: env.E2E_TEST_REPO_URL,
        });
        projectId = project.id;
        projBank = `proj-${projectId}`;

        // The fixture repo carries README.md + docs/gm-coin.md + docs/icp-backend.md
        await expect.poll(async () => {
            const banks = await dumpBanks();
            const mems = banks[projBank] || [];
            return mems.map((m: any) => m.document_id).sort();
        }, { timeout: 120_000, intervals: [2000], message: 'project docs should be ingested into the mock' })
            .toEqual(['doc:README.md', 'doc:docs/gm-coin.md', 'doc:docs/icp-backend.md']);

        const banks = await dumpBanks();
        const mems = banks[projBank];
        const gmCoin = mems.find((m: any) => m.document_id === 'doc:docs/gm-coin.md');
        expect(gmCoin.text).toContain('GM Coin is a community token');
        expect(gmCoin.tags).toContain(`project:${projectId}`);
        expect(gmCoin.tags).toContain('source:docs');
        expect(gmCoin.type).toBe('world');

        // Served back through the server's /api/memory proxy.
        const banksRes = await request.get(`/api/memory/banks?company_id=${companyId}`);
        expect(banksRes.ok()).toBeTruthy();
        const bankList = await banksRes.json();
        expect((bankList as any[]).map((b: any) => b.bank_id)).toEqual(
            expect.arrayContaining([projBank, runsBank]));

        const listRes = await request.get(`/api/memory/banks/${projBank}/memories?limit=50`);
        expect(listRes.ok()).toBeTruthy();
        const listBody = await listRes.json();
        expect(listBody.total).toBe(3);
        expect((listBody.items as any[]).some((i: any) => i.text.includes('Internet Computer'))).toBeTruthy();

        const statsRes = await request.get(`/api/memory/banks/${projBank}/stats`);
        expect(statsRes.ok()).toBeTruthy();
        expect((await statsRes.json()).memory_units).toBe(3);
    });

    test('doc changes are tracked: settings sync upserts the changed document', async ({ request }) => {
        // The server scans the project's cloned working copy on disk.
        const repoPath = path.join(paperclipBase, 'data', 'artifacts', shortName, projectName);
        const icpDoc = path.join(repoPath, 'docs', 'icp-backend.md');
        expect(fs.existsSync(icpDoc), `expected cloned repo doc at ${icpDoc}`).toBeTruthy();

        fs.writeFileSync(icpDoc,
            '# ICP backend\n\nThe GM Coin backend now uses Rust canisters on the Internet Computer. ' +
            'Ledger state migrated from Motoko to Rust for performance.\n');

        const syncRes = await request.post('/api/settings/sync');
        expect(syncRes.ok()).toBeTruthy();

        // Upsert: exactly one memory for the document, carrying the new text.
        await expect.poll(async () => {
            const banks = await dumpBanks();
            const docMems = (banks[projBank] || []).filter((m: any) => m.document_id === 'doc:docs/icp-backend.md');
            if (docMems.length !== 1) return `count=${docMems.length}`;
            return docMems[0].text.includes('Rust canisters') ? 'updated' : 'stale';
        }, { timeout: 120_000, intervals: [2000], message: 'changed doc should be re-retained (upserted)' })
            .toBe('updated');

        const banks = await dumpBanks();
        expect((banks[projBank] || []).length).toBe(3); // no duplicates
    });

    test('CTO run failure is retained as experience memory', async ({ request }) => {
        const sprint = await postJSON(request, '/api/sprints', {
            company_id: companyId, name: 'Memory Sprint',
        });
        sprintId = sprint.id;

        const task1 = await postJSON(request, '/api/tasks', {
            company_id: companyId,
            project_id: projectId,
            sprint_id: sprintId,
            agent_id: agentId,
            agent_config_name: 'CTO',
            title: 'Implement ICP as backend for GM Coin',
            description: 'Implement the ICP backend for GM Coin as documented.',
            task_type: 'implement',
        });
        task1Id = task1.id;

        await resetProviderMock();
        await setScenario([
            { tool_call: { id: 'cto1', name: 'memory_recall', arguments: { query: 'ICP backend GM Coin' } } },
            { tool_call: { id: 'cto2', name: 'finish_task', arguments: {
                task_status: 'blocked',
                finish_status: 'Blocked: no shell access during task execution',
                result_details: 'Could not implement the ICP backend: no shell access during task execution.',
            } } },
        ]);

        const upd = await request.put(`/api/tasks/${task1Id}`, { data: { status: 'to-do' } });
        expect(upd.ok()).toBeTruthy();
        await waitForTaskStatus(request, task1Id, 'blocked', 90_000);

        // memory_recall in the CTO run saw the project docs.
        const results = await toolResults();
        expect(results.some((c) => c.includes('Internet Computer') || c.includes('Rust canisters')),
            'CTO memory_recall should surface project doc memories').toBeTruthy();

        // Run-outcome retention is async — poll the mock.
        await expect.poll(async () => {
            const banks = await dumpBanks();
            return (banks[runsBank] || []).filter((m: any) =>
                m.tags.includes('agent:cto') && m.text.includes('no shell access during task execution')).length;
        }, { timeout: 60_000, message: 'CTO blocked-run memory should land in the runs bank' }).toBeGreaterThan(0);

        const banks = await dumpBanks();
        const ctoMem = (banks[runsBank] || []).find((m: any) => m.tags.includes('agent:cto'));
        expect(ctoMem.type).toBe('experience');
        expect(ctoMem.document_id).toMatch(/^run-\d+$/);
        expect(ctoMem.metadata.agent).toBe('CTO');
        expect(ctoMem.tags.some((t: string) => t.startsWith('session:'))).toBeTruthy();
        expect(ctoMem.tags.some((t: string) => t.startsWith('task:'))).toBeTruthy();
    });

    test('CMO recall surfaces the CTO failure; CMO outcome retained', async ({ request }) => {
        const task2 = await postJSON(request, '/api/tasks', {
            company_id: companyId,
            project_id: projectId,
            sprint_id: sprintId,
            agent_id: agentId,
            agent_config_name: 'CMO',
            title: 'Write X post about ICP backend',
            description: 'Write an X post announcing the ICP backend of GM Coin.',
            task_type: 'implement',
        });
        task2Id = task2.id;

        await resetProviderMock();
        await setScenario([
            { tool_call: { id: 'cmo1', name: 'memory_recall', arguments: { query: 'ICP backend implementation status for GM Coin' } } },
            { tool_call: { id: 'cmo2', name: 'finish_task', arguments: {
                task_status: 'blocked',
                finish_status: 'Blocked: no implementation yet',
                result_details: 'Cannot announce the ICP backend — there is no implementation yet (CTO was blocked).',
            } } },
        ]);

        const upd = await request.put(`/api/tasks/${task2Id}`, { data: { status: 'to-do' } });
        expect(upd.ok()).toBeTruthy();
        await waitForTaskStatus(request, task2Id, 'blocked', 90_000);

        // The memory_recall tool RESULT the server returned to the CMO must
        // contain the CTO's blocked/error memory.
        const results = await toolResults();
        expect(results.some((c) => c.includes('no shell access during task execution')),
            'CMO memory_recall result should contain the CTO error memory').toBeTruthy();

        await expect.poll(async () => {
            const banks = await dumpBanks();
            return (banks[runsBank] || []).filter((m: any) =>
                m.tags.includes('agent:cmo') && m.text.includes('no implementation yet')).length;
        }, { timeout: 60_000, message: 'CMO blocked-run memory should be retained' }).toBeGreaterThan(0);
    });

    test('rerun: CTO sees its previous failure, then succeeds', async ({ request }) => {
        await resetProviderMock();
        await setScenario([
            { tool_call: { id: 'cto3', name: 'memory_recall', arguments: { query: 'previous attempts errors ICP backend GM Coin', agent: 'CTO' } } },
            { tool_call: { id: 'cto4', name: 'finish_task', arguments: {
                task_status: 'done',
                finish_status: 'ICP backend implemented',
                result_details: 'ICP backend implemented for GM Coin with Rust canisters; shell access restored.',
            } } },
        ]);

        const rerun = await request.post(`/api/tasks/${task1Id}/rerun`);
        expect(rerun.ok()).toBeTruthy();
        await waitForTaskStatus(request, task1Id, 'done', 90_000);

        const results = await toolResults();
        expect(results.some((c) => c.includes('no shell access during task execution')),
            'CTO rerun memory_recall should surface the previous error memory').toBeTruthy();

        await expect.poll(async () => {
            const banks = await dumpBanks();
            return (banks[runsBank] || []).filter((m: any) =>
                m.tags.includes('agent:cto') && m.text.includes('ICP backend implemented')).length;
        }, { timeout: 60_000, message: 'CTO success memory should be retained' }).toBeGreaterThan(0);

        // The old failure memory is a separate run document and must still exist.
        const banks = await dumpBanks();
        expect((banks[runsBank] || []).some((m: any) => m.text.includes('no shell access during task execution'))).toBeTruthy();
    });

    test('rerun: CMO now sees the CTO success and writes the post', async ({ request }) => {
        await resetProviderMock();
        await setScenario([
            { tool_call: { id: 'cmo3', name: 'memory_recall', arguments: { query: 'ICP backend implementation status for GM Coin' } } },
            { tool_call: { id: 'cmo4', name: 'finish_task', arguments: {
                task_status: 'done',
                finish_status: 'X post published about the ICP backend',
                result_details: 'Published: "GM Coin now runs on the Internet Computer — ICP backend live!"',
            } } },
        ]);

        const rerun = await request.post(`/api/tasks/${task2Id}/rerun`);
        expect(rerun.ok()).toBeTruthy();
        await waitForTaskStatus(request, task2Id, 'done', 90_000);

        const results = await toolResults();
        expect(results.some((c) => c.includes('ICP backend implemented')),
            'CMO rerun memory_recall should surface the CTO success memory').toBeTruthy();

        await expect.poll(async () => {
            const banks = await dumpBanks();
            return (banks[runsBank] || []).filter((m: any) =>
                m.tags.includes('agent:cmo') && m.text.includes('X post published')).length;
        }, { timeout: 60_000, message: 'CMO success memory should be retained' }).toBeGreaterThan(0);
    });

    test('memory UI API surface: list, patch, delete, recall, ask, graph', async ({ request }) => {
        // Banks list
        const bankList = await (await request.get(`/api/memory/banks?company_id=${companyId}`)).json();
        expect((bankList as any[]).length).toBeGreaterThanOrEqual(2);

        // List memories of the runs bank
        const listBody = await (await request.get(`/api/memory/banks/${runsBank}/memories?limit=50`)).json();
        expect(listBody.total).toBeGreaterThanOrEqual(4); // 2 CTO + 2 CMO runs
        const target = (listBody.items as any[]).find((i: any) => i.text.includes('no implementation yet'));
        expect(target).toBeTruthy();

        // Single memory GET
        const single = await (await request.get(`/api/memory/banks/${runsBank}/memories/${target.id}`)).json();
        expect(single.id).toBe(target.id);

        // PATCH text
        const patched = await (await request.patch(`/api/memory/banks/${runsBank}/memories/${target.id}`, {
            data: { text: 'CURATED: outdated CMO note about missing implementation' },
        })).json();
        expect(patched.text).toContain('CURATED');
        const afterPatch = await (await request.get(`/api/memory/banks/${runsBank}/memories/${target.id}`)).json();
        expect(afterPatch.text).toContain('CURATED');

        // Recall finds the curated memory
        const recall1 = await (await request.post(`/api/memory/banks/${runsBank}/recall`, {
            data: { query: 'CURATED outdated note' },
        })).json();
        expect((recall1.results as any[]).some((r: any) => r.id === target.id)).toBeTruthy();

        // DELETE soft-invalidates: state flips and recall excludes it
        const del = await request.delete(`/api/memory/banks/${runsBank}/memories/${target.id}`);
        expect(del.ok()).toBeTruthy();
        expect((await del.json()).state).toBe('invalidated');
        const afterDel = await (await request.get(`/api/memory/banks/${runsBank}/memories/${target.id}`)).json();
        expect(afterDel.state).toBe('invalidated');
        const recall2 = await (await request.post(`/api/memory/banks/${runsBank}/recall`, {
            data: { query: 'CURATED outdated note' },
        })).json();
        expect((recall2.results as any[]).some((r: any) => r.id === target.id)).toBeFalsy();

        // Recall over run experience
        const recall3 = await (await request.post(`/api/memory/banks/${runsBank}/recall`, {
            data: { query: 'ICP backend GM Coin' },
        })).json();
        expect((recall3.results as any[]).length).toBeGreaterThan(0);

        // Ask (reflect)
        const ask = await (await request.post(`/api/memory/banks/${projBank}/ask`, {
            data: { query: 'What backend does GM Coin use?' },
        })).json();
        expect(ask.text).toContain('Based on stored memories:');
        expect(ask.text).toContain('Internet Computer');

        // Graph + entities graph
        const graph = await (await request.get(`/api/memory/banks/${projBank}/graph?limit=100`)).json();
        expect(graph.total_units).toBe(3);
        expect((graph.nodes as any[]).length).toBe(3);
        const eg = await (await request.get(`/api/memory/banks/${projBank}/entities-graph`)).json();
        expect(eg.nodes).toEqual([]);

        // Project memory sync endpoint (no-op here, but must respond OK)
        const syncRes = await request.post(`/api/memory/projects/${projectId}/sync`);
        expect(syncRes.ok()).toBeTruthy();
    });
});

async function postJSON(request: APIRequestContext, url: string, data: unknown): Promise<any> {
    const res = await request.post(url, { data });
    if (!res.ok()) {
        throw new Error(`POST ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}
