import { test, expect, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';

const env = loadE2EEnv();

/**
 * Hindsight memory layer, end-to-end against the mock Hindsight server:
 *
 *   a. project doc ingestion (.md files of the git repo → the single
 *      per-company bank company-<companyID>)
 *   b. doc change tracking via POST /api/memory/projects/{id}/sync (document upsert)
 *   c. agent experience: CTO fails a task, its error memory surfaces in a
 *      later CMO run via memory_recall; reruns then surface the history and
 *      add success memories (same bank company-<companyID>, tags agent:*)
 *   d. the /api/memory proxy surface (list, patch, delete, recall, ask)
 *
 * Since Phase 1 of the memory-layer upgrade, all memories for a company
 * (project docs and agent run outcomes alike) live in a single bank named
 * company-<companyID>; there is no longer a separate per-project doc bank.
 */
test.describe.serial('Memory (Hindsight) layer', () => {
    const shortName = 'memco';
    const projectName = 'GM Coin';
    const headcount1Base = path.join(env.E2E_HEADCOUNT1_HOME, '.headcount1');

    let companyId: number;
    let projectId: number;
    let providerId: number;
    let agentId: number;
    let sprintId: number;
    let task1Id: number; // CTO task
    let task2Id: number; // CMO task
    let bank: string;

    const cleanFilesystem = () => {
        for (const root of ['repos', 'workspace', 'artifacts', 'logs', 'skills']) {
            const fullPath = path.join(headcount1Base, root, shortName);
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }
    };

    const dumpAll = async (): Promise<any> => {
        const res = await fetch(`${env.E2E_HINDSIGHT_URL}/__admin/dump`);
        expect(res.ok).toBeTruthy();
        return res.json();
    };

    const dumpBanks = async (): Promise<Record<string, any[]>> => {
        return (await dumpAll()).banks as Record<string, any[]>;
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
        // wipe-db fully resets the memory layer's server-side state too: it
        // clears hindsight_documents AND the Go process's in-memory
        // "already ensured" guards for bank config / mental models
        // (Service.ResetEnsured, called from WipeDB), so reused company IDs —
        // whether from earlier spec files or a serial-mode retry of this one —
        // get their bank re-configured from scratch against the reset mock.
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

    test('project docs are ingested into the company memory bank', async ({ request }) => {
        const company = await postJSON(request, '/api/companies', {
            name: 'Memory Co', short_name: shortName, color: '#0ea5e9',
        });
        companyId = company.id;
        bank = `company-${companyId}`;

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

        // The fixture repo carries README.md + docs/gm-coin.md + docs/icp-backend.md,
        // now retained with a project-id prefix so multiple projects' docs can
        // coexist in the single company bank.
        await expect.poll(async () => {
            const banks = await dumpBanks();
            const mems = banks[bank] || [];
            return mems.map((m: any) => m.document_id).sort();
        }, { timeout: 120_000, intervals: [2000], message: 'project docs should be ingested into the mock' })
            .toEqual([
                `doc:${projectId}/README.md`,
                `doc:${projectId}/docs/gm-coin.md`,
                `doc:${projectId}/docs/icp-backend.md`,
            ]);

        const banks = await dumpBanks();
        const mems = banks[bank];
        const gmCoin = mems.find((m: any) => m.document_id === `doc:${projectId}/docs/gm-coin.md`);
        expect(gmCoin.text).toContain('GM Coin is a community token');
        expect(gmCoin.tags).toContain('project:gm-coin'); // tag is the project name slug, not the id
        expect(gmCoin.tags).toContain('source:docs');
        expect(gmCoin.type).toBe('world');

        // Served back through the server's /api/memory proxy: exactly one
        // bank per company now (Phase 1 bank consolidation).
        const banksRes = await request.get(`/api/memory/banks?company_id=${companyId}`);
        expect(banksRes.ok()).toBeTruthy();
        const bankList = await banksRes.json();
        expect(bankList).toEqual([
            { bank_id: bank, kind: 'company', label: 'Memory — Memory Co' },
        ]);

        const listRes = await request.get(`/api/memory/banks/${bank}/memories?limit=50`);
        expect(listRes.ok()).toBeTruthy();
        const listBody = await listRes.json();
        expect(listBody.total).toBe(3);
        expect((listBody.items as any[]).some((i: any) => i.text.includes('Internet Computer'))).toBeTruthy();

        const statsRes = await request.get(`/api/memory/banks/${bank}/stats`);
        expect(statsRes.ok()).toBeTruthy();
        expect((await statsRes.json()).memory_units).toBe(3);

        // EnsureBank ran (once per company per process, guarded server-side)
        // as part of doc sync: the bank config was PATCHed with a mission
        // naming the company and a skepticism disposition, and both standing
        // directives exist exactly once.
        const dump1 = await dumpAll();
        expect(dump1.configs[bank].reflect_mission).toContain('Memory Co');
        expect(dump1.configs[bank].disposition_skepticism).toBe(4);
        const directiveNames1 = (dump1.directives[bank] as any[]).map((d) => d.name).sort();
        expect(directiveNames1).toEqual(['cite-source', 'no-false-completion']);
    });

    test('doc changes are tracked: project memory sync upserts the changed document', async ({ request }) => {
        // The server scans the project's cloned working copy on disk.
        const repoPath = path.join(headcount1Base, 'repos', shortName, projectName);
        const icpDoc = path.join(repoPath, 'docs', 'icp-backend.md');
        expect(fs.existsSync(icpDoc), `expected cloned repo doc at ${icpDoc}`).toBeTruthy();

        fs.writeFileSync(icpDoc,
            '# ICP backend\n\nThe GM Coin backend now uses Rust canisters on the Internet Computer. ' +
            'Ledger state migrated from Motoko to Rust for performance.\n');

        const syncRes = await request.post(`/api/memory/projects/${projectId}/sync`);
        expect(syncRes.ok()).toBeTruthy();

        // Upsert: exactly one memory for the document, carrying the new text.
        await expect.poll(async () => {
            const banks = await dumpBanks();
            const docMems = (banks[bank] || []).filter((m: any) => m.document_id === `doc:${projectId}/docs/icp-backend.md`);
            if (docMems.length !== 1) return `count=${docMems.length}`;
            return docMems[0].text.includes('Rust canisters') ? 'updated' : 'stale';
        }, { timeout: 120_000, intervals: [2000], message: 'changed doc should be re-retained (upserted)' })
            .toBe('updated');

        const banks = await dumpBanks();
        expect((banks[bank] || []).length).toBe(3); // no duplicates

        // EnsureBank is guarded to run once per company per process: a
        // second retain (this doc re-sync) must not create duplicate
        // directives or re-apply the config in a way that duplicates state.
        const dump2 = await dumpAll();
        const directiveNames2 = (dump2.directives[bank] as any[]).map((d: any) => d.name).sort();
        expect(directiveNames2).toEqual(['cite-source', 'no-false-completion']);
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
            return (banks[bank] || []).filter((m: any) =>
                m.tags.includes('agent:cto') && m.text.includes('no shell access during task execution')).length;
        }, { timeout: 60_000, message: 'CTO blocked-run memory should land in the company bank' }).toBeGreaterThan(0);

        // Every recall (memory_recall tool + pre-task briefing) requests
        // observation-aware recall: consolidated observations preferred
        // alongside raw world/experience facts.
        const dump3 = await dumpAll();
        const lastRecall = dump3.last_recall[bank];
        expect(lastRecall).toBeTruthy();
        expect(lastRecall.types).toEqual(expect.arrayContaining(['world', 'experience', 'observation']));
        expect(lastRecall.prefer_observations).toBe(true);

        // The engine lazily ensures a project-state mental model on the next
        // task run and refreshes it via the retain that just happened
        // (project tag overlap) — poll for it surfacing the blocker.
        await expect.poll(async () => {
            const res = await request.get(`/api/memory/banks/${bank}/mental-models/project-state-${projectId}`);
            if (!res.ok()) return null;
            return (await res.json()).content as string;
        }, { timeout: 60_000, message: 'project-state mental model should synthesize the CTO blocker' })
            .toEqual(expect.stringContaining('no shell access during task execution'));

        const banks = await dumpBanks();
        const ctoMem = (banks[bank] || []).find((m: any) => m.tags.includes('agent:cto'));
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
            return (banks[bank] || []).filter((m: any) =>
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
            return (banks[bank] || []).filter((m: any) =>
                m.tags.includes('agent:cto') && m.text.includes('ICP backend implemented')).length;
        }, { timeout: 60_000, message: 'CTO success memory should be retained' }).toBeGreaterThan(0);

        // The old failure memory is a separate run document and must still exist.
        const banks = await dumpBanks();
        expect((banks[bank] || []).some((m: any) => m.text.includes('no shell access during task execution'))).toBeTruthy();

        // The project-state mental model refreshes again on this retain
        // (same project tag) and now synthesizes the success instead.
        await expect.poll(async () => {
            const res = await request.get(`/api/memory/banks/${bank}/mental-models/project-state-${projectId}`);
            if (!res.ok()) return null;
            return (await res.json()).content as string;
        }, { timeout: 60_000, message: 'project-state mental model should refresh to mention the CTO success' })
            .toEqual(expect.stringContaining('ICP backend implemented'));
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
            return (banks[bank] || []).filter((m: any) =>
                m.tags.includes('agent:cmo') && m.text.includes('X post published')).length;
        }, { timeout: 60_000, message: 'CMO success memory should be retained' }).toBeGreaterThan(0);
    });

    test('memory UI API surface: list, patch, delete, recall, ask, graph', async ({ request }) => {
        // Banks list: still exactly one bank per company.
        const bankList = await (await request.get(`/api/memory/banks?company_id=${companyId}`)).json();
        expect((bankList as any[]).length).toBe(1);
        expect((bankList as any[])[0].bank_id).toBe(bank);

        // List memories of the (single, shared) company bank: 3 docs + at
        // least 4 run-outcome memories (2 CTO + 2 CMO runs).
        const listBody = await (await request.get(`/api/memory/banks/${bank}/memories?limit=50`)).json();
        expect(listBody.total).toBeGreaterThanOrEqual(7);
        const target = (listBody.items as any[]).find((i: any) => i.text.includes('no implementation yet'));
        expect(target).toBeTruthy();

        // Single memory GET
        const single = await (await request.get(`/api/memory/banks/${bank}/memories/${target.id}`)).json();
        expect(single.id).toBe(target.id);

        // PATCH text
        const patched = await (await request.patch(`/api/memory/banks/${bank}/memories/${target.id}`, {
            data: { text: 'CURATED: outdated CMO note about missing implementation' },
        })).json();
        expect(patched.text).toContain('CURATED');
        const afterPatch = await (await request.get(`/api/memory/banks/${bank}/memories/${target.id}`)).json();
        expect(afterPatch.text).toContain('CURATED');

        // Recall finds the curated memory
        const recall1 = await (await request.post(`/api/memory/banks/${bank}/recall`, {
            data: { query: 'CURATED outdated note' },
        })).json();
        expect((recall1.results as any[]).some((r: any) => r.id === target.id)).toBeTruthy();

        // DELETE soft-invalidates: state flips and recall excludes it
        const del = await request.delete(`/api/memory/banks/${bank}/memories/${target.id}`);
        expect(del.ok()).toBeTruthy();
        expect((await del.json()).state).toBe('invalidated');
        const afterDel = await (await request.get(`/api/memory/banks/${bank}/memories/${target.id}`)).json();
        expect(afterDel.state).toBe('invalidated');
        const recall2 = await (await request.post(`/api/memory/banks/${bank}/recall`, {
            data: { query: 'CURATED outdated note' },
        })).json();
        expect((recall2.results as any[]).some((r: any) => r.id === target.id)).toBeFalsy();

        // Recall over run experience
        const recall3 = await (await request.post(`/api/memory/banks/${bank}/recall`, {
            data: { query: 'ICP backend GM Coin' },
        })).json();
        expect((recall3.results as any[]).length).toBeGreaterThan(0);

        // Ask (reflect)
        const ask = await (await request.post(`/api/memory/banks/${bank}/ask`, {
            data: { query: 'What backend does GM Coin use?' },
        })).json();
        expect(ask.text).toContain('Based on stored memories:');
        expect(ask.text).toContain('Internet Computer');

        // Graph + entities graph: reflects everything currently in the shared bank.
        const banksNow = await dumpBanks();
        const expectedUnits = (banksNow[bank] || []).length;
        const graph = await (await request.get(`/api/memory/banks/${bank}/graph?limit=100`)).json();
        expect(graph.total_units).toBe(expectedUnits);
        expect((graph.nodes as any[]).length).toBe(expectedUnits);
        const eg = await (await request.get(`/api/memory/banks/${bank}/entities-graph`)).json();
        expect(eg.nodes).toEqual([]);

        // Project memory sync endpoint: docs on disk are unchanged since the
        // last sync, so this must be a genuine no-op, not just a 200.
        const syncRes = await request.post(`/api/memory/projects/${projectId}/sync`);
        expect(syncRes.ok()).toBeTruthy();
        const syncBody = await syncRes.json();
        expect(syncBody.added).toBe(0);
        expect(syncBody.updated).toBe(0);
        expect(syncBody.removed).toBe(0);
    });

    test('memory UI API surface: config, directives, mental models (Phase 2/3 proxies)', async ({ request }) => {
        const modelId = `project-state-${projectId}`;

        // GET bank config: reflects EnsureBank's mission + disposition.
        const configRes = await request.get(`/api/memory/banks/${bank}/config`);
        expect(configRes.ok()).toBeTruthy();
        const configBody = await configRes.json();
        expect(configBody.config.reflect_mission).toContain('Memory Co');
        expect(configBody.config.disposition_skepticism).toBe(4);

        // GET directives: both standing directives, exactly once each.
        const directivesRes = await request.get(`/api/memory/banks/${bank}/directives`);
        expect(directivesRes.ok()).toBeTruthy();
        const directivesBody = await directivesRes.json();
        expect((directivesBody.items as any[]).map((d) => d.name).sort())
            .toEqual(['cite-source', 'no-false-completion']);

        // GET mental-models list: the project-state model exists by now
        // (created lazily by the CTO's task runs above).
        const listRes = await request.get(`/api/memory/banks/${bank}/mental-models`);
        expect(listRes.ok()).toBeTruthy();
        const listBody = await listRes.json();
        expect((listBody.items as any[]).some((m: any) => m.id === modelId)).toBeTruthy();

        // GET single mental model.
        const modelRes = await request.get(`/api/memory/banks/${bank}/mental-models/${modelId}`);
        expect(modelRes.ok()).toBeTruthy();
        const modelBody = await modelRes.json();
        expect(modelBody.id).toBe(modelId);
        expect(typeof modelBody.content).toBe('string');

        // POST refresh: accepted, and the model's last_refreshed_at is sane.
        const refreshRes = await request.post(`/api/memory/banks/${bank}/mental-models/${modelId}/refresh`);
        expect(refreshRes.ok()).toBeTruthy();
        const afterRefresh = await (await request.get(`/api/memory/banks/${bank}/mental-models/${modelId}`)).json();
        expect(afterRefresh.last_refreshed_at).toBeTruthy();

        // DELETE removes it; subsequent GET 404s.
        const deleteRes = await request.delete(`/api/memory/banks/${bank}/mental-models/${modelId}`);
        expect(deleteRes.ok()).toBeTruthy();
        const afterDeleteRes = await request.get(`/api/memory/banks/${bank}/mental-models/${modelId}`);
        expect(afterDeleteRes.ok()).toBeFalsy();
    });

    test('memory UI API surface: mental model create/edit/history and tags', async ({ request }) => {
        // Validation: name and source_query are required by the server proxy.
        const missingName = await request.post(`/api/memory/banks/${bank}/mental-models`, {
            data: { source_query: 'no name given' },
        });
        expect(missingName.status()).toBe(400);
        const missingQuery = await request.post(`/api/memory/banks/${bank}/mental-models`, {
            data: { name: 'No query' },
        });
        expect(missingQuery.status()).toBe(400);

        // Create with a custom deterministic id (Hindsight allows
        // lowercase-hyphenated custom ids; the response echoes it).
        const modelId = 'custom-cto-focus';
        const createRes = await request.post(`/api/memory/banks/${bank}/mental-models`, {
            data: {
                id: modelId,
                name: 'CTO focus',
                source_query: 'What is the CTO currently working on and struggling with?',
                tags: ['agent:cto'],
                max_tokens: 512,
            },
        });
        expect(createRes.ok()).toBeTruthy();
        const created = await createRes.json();
        expect(created.mental_model_id).toBe(modelId);

        const model1 = await (await request.get(`/api/memory/banks/${bank}/mental-models/${modelId}`)).json();
        expect(model1.name).toBe('CTO focus');
        // The bank already holds CTO experience memories, so the synthesis
        // draws on them right away.
        expect(model1.content).toContain('ICP backend implemented');

        // PATCH: only the provided fields change.
        const patchRes = await request.patch(`/api/memory/banks/${bank}/mental-models/${modelId}`, {
            data: { name: 'CTO focus (renamed)' },
        });
        expect(patchRes.ok()).toBeTruthy();
        const model2 = await (await request.get(`/api/memory/banks/${bank}/mental-models/${modelId}`)).json();
        expect(model2.name).toBe('CTO focus (renamed)');
        expect(model2.source_query).toBe('What is the CTO currently working on and struggling with?');
        expect(model2.tags).toEqual(['agent:cto']);

        // Refresh, then history: one snapshot from creation + one from the
        // explicit refresh.
        const refreshRes = await request.post(`/api/memory/banks/${bank}/mental-models/${modelId}/refresh`);
        expect(refreshRes.ok()).toBeTruthy();
        const historyRes = await request.get(`/api/memory/banks/${bank}/mental-models/${modelId}/history`);
        expect(historyRes.ok()).toBeTruthy();
        // Real Hindsight returns a bare array of {previous_content, changed_at}.
        const history = await historyRes.json();
        expect(Array.isArray(history)).toBeTruthy();
        expect((history as any[]).length).toBeGreaterThanOrEqual(2);
        for (const entry of history as any[]) {
            expect(typeof entry.previous_content).toBe('string');
            expect(entry.changed_at).toBeTruthy();
        }

        // Tags listing: every scoping level used by the memory layer shows up
        // with a usage count (doc tags + run tags; only active memories count).
        const tagsRes = await request.get(`/api/memory/banks/${bank}/tags`);
        expect(tagsRes.ok()).toBeTruthy();
        const tags = await tagsRes.json();
        const byTag = new Map((tags.items as any[]).map((t: any) => [t.tag, t.count]));
        for (const expected of ['source:docs', 'project:gm-coin', 'agent:cto', 'agent:cmo']) {
            expect(byTag.get(expected), `tag ${expected} should be listed`).toBeGreaterThan(0);
        }
        expect(byTag.get('source:docs')).toBe(3);
    });
});

async function postJSON(request: APIRequestContext, url: string, data: unknown): Promise<any> {
    const res = await request.post(url, { data });
    if (!res.ok()) {
        throw new Error(`POST ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}
