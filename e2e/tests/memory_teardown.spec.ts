import { test, expect, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';

const env = loadE2EEnv();

// Unique markers so recall assertions can't match anything else.
const ARTIFACT_MARKER = 'ZEBRA-ARTIFACT-9481 the payment webhook must be verified with HMAC before processing';
const BLOCKER_FACT = 'Build needs CGO_ENABLED=0 on alpine images, otherwise the linker fails with missing libc symbols';
const BLOCKER_PARAPHRASE = 'IMPORTANT LEARNING: on alpine the compilation breaks with libc linker errors unless CGO_ENABLED is set to 0';
const DIFFERENT_FACT = 'Agents lack browser tools, so web UI testing has to be delegated to the QA browser worker';

/**
 * MemPalace Phase 1.5 — run-teardown capture, dedup and protocol slimming,
 * end-to-end through the real engine, real palace and a scripted mock LLM.
 *
 * Covers the pilot regressions:
 *   1. Crash amnesia (runs 91/93): a CANCELED run — one that never reaches
 *      finish_task — must still leave an auto-diary, ingested artifacts,
 *      a status KG fact and a mined transcript behind.
 *   2. Paraphrase duplicates: a re-phrased fact is rejected as a duplicate
 *      (with the existing text surfaced), while a genuinely different fact
 *      sharing vocabulary is stored.
 *   3. Protocol slimming: no write_diary in the tool listing, new remember
 *      guidance in the system prompt.
 */
test.describe.serial('MemPalace teardown capture & dedup', () => {
    let companyId: number;
    let agentId: number;
    let sprintId: number;
    let providerId: number;

    const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');

    const cleanFilesystem = () => {
        for (const subDir of ['data/td-co', 'companies/td-co', 'workspace/td-co', 'memory/td-co']) {
            const fullPath = path.join(paperclipBase, subDir);
            if (fs.existsSync(fullPath)) fs.rmSync(fullPath, { recursive: true, force: true });
        }
        if (providerId) {
            const providerFile = path.join(paperclipBase, 'data', 'llm-providers', `${providerId}.json`);
            if (fs.existsSync(providerFile)) fs.rmSync(providerFile, { force: true });
        }
    };

    test.beforeAll(async ({ request }) => {
        cleanFilesystem();
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
    });

    test.afterAll(async ({ request }) => {
        cleanFilesystem();
        await request.post('/api/e2e/wipe-db');
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
    });

    test('setup: provider, company, memory ready', async ({ request }) => {
        test.setTimeout(360_000);

        const provider = await postJSON(request, '/api/providers', {
            name: 'e2e-td-mock',
            base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key',
            provider_type: 'openai',
            default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model',
        });
        providerId = provider.id;

        const company = await postJSON(request, '/api/companies', {
            name: 'Teardown Co', short_name: 'td-co', color: '#0ea5e9',
        });
        companyId = company.id;

        const agent = await postJSON(request, '/api/agents', {
            company_id: companyId,
            name: 'Worker',
            system_prompt: 'You do the work.',
            model: 'e2e-mock-model',
            provider_id: provider.id,
        });
        agentId = agent.id;

        const sprint = await postJSON(request, '/api/sprints', {
            company_id: companyId, name: 'Teardown Sprint',
        });
        sprintId = sprint.id;

        await expect
            .poll(async () => {
                const res = await request.get(`/api/memory/status?company_id=${companyId}`);
                if (!res.ok()) return 'status-error';
                const body = await res.json();
                return body.available && body.ready ? 'ready' : `waiting (${JSON.stringify(body)})`;
            }, { timeout: 300_000, intervals: [5_000] })
            .toBe('ready');
    });

    async function createTask(
        request: APIRequestContext, title: string, entries: object[], agentConfig?: string,
    ): Promise<number> {
        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ entries }),
        });
        const task = await postJSON(request, '/api/tasks', {
            company_id: companyId,
            sprint_id: sprintId,
            agent_id: agentId,
            title,
            description: `${title} (e2e teardown scenario)`,
            task_type: 'implement',
            ...(agentConfig ? { agent_config_name: agentConfig } : {}),
        });
        await putJSON(request, `/api/tasks/${task.id}`, { status: 'to-do' });
        return task.id;
    }

    let canceledTaskId: number;
    let canceledTaskRef: string;

    test('canceled run still captures memory (crash-amnesia regression)', async ({ request }) => {
        test.setTimeout(420_000);

        // The scripted agent writes an artifact, then calls ask_human — a
        // blocking tool — so the run parks deterministically mid-session and
        // never reaches finish_task. We then cancel it via the API. The task
        // is pinned to the CTO config: unpinned root tasks route to the CEO
        // orchestrator, which has neither write_artifact nor file tools.
        canceledTaskId = await createTask(request, 'Doomed payment webhook task', [
            {
                tool_call: {
                    id: 'a1', name: 'write_artifact',
                    arguments: {
                        filename: 'webhook-notes.md',
                        content: `# Webhook design\n\n${ARTIFACT_MARKER}\n`,
                        description: 'security decision notes for the webhook',
                    },
                },
            },
            { tool_call: { id: 'a2', name: 'ask_human', arguments: { question: 'Should I continue? (e2e will cancel here)' } } },
        ], 'CTO');

        // Wait until the run exists and has produced the artifact, then stop it.
        let runId = 0;
        await expect.poll(async () => {
            const runs = await (await request.get(`/api/runs?company_id=${companyId}`)).json();
            const run = (runs || []).find((r: { task_id: number }) => r.task_id === canceledTaskId);
            runId = run?.id ?? 0;
            return run?.status ?? 'none';
        }, { timeout: 120_000, intervals: [2_000] }).toBe('running');

        await expect.poll(async () => {
            const arts = await (await request.get(`/api/tasks/${canceledTaskId}/artifacts`)).json();
            return (arts || []).length;
        }, { timeout: 120_000, intervals: [2_000] }).toBeGreaterThan(0);

        await postJSON(request, `/api/runs/${runId}/stop`, {});

        await expect.poll(async () => {
            const run = await (await request.get(`/api/runs/${runId}`)).json();
            return run.status;
        }, { timeout: 60_000, intervals: [2_000] }).toBe('canceled');

        const task = await (await request.get(`/api/tasks/${canceledTaskId}`)).json();
        canceledTaskRef = String(task.ref_key || '').toLowerCase();
        expect(canceledTaskRef).toBeTruthy();

        // Teardown runs detached — poll the activity feed for its traces.
        // The first palace writes also warm the embedding model, so be patient.
        await expect.poll(async () => {
            const activity = await (await request.get(`/api/memory/activity?company_id=${companyId}&limit=200`)).json();
            const tools = (activity.items || []).map((a: { tool: string }) => a.tool);
            const want = ['engine:auto-diary', 'engine:artifact-ingest', 'engine:kg-facts', 'engine:transcript-mine'];
            const missing = want.filter((t) => !tools.includes(t));
            return missing.length === 0 ? 'all' : `missing: ${missing.join(',')}`;
        }, { timeout: 240_000, intervals: [5_000] }).toBe('all');

        // Engine-initiated rows carry no agent name.
        const activity = await (await request.get(`/api/memory/activity?company_id=${companyId}&limit=200`)).json();
        const diaryRow = activity.items.find((a: { tool: string }) => a.tool === 'engine:auto-diary');
        expect(diaryRow.agent_name).toBeFalsy();

        // The transcript mine must have actually succeeded (result_n=1), not
        // just left an error row — a CLI-vs-MCP palace lock conflict once
        // produced result_n=0 here.
        const mineRow = activity.items.find((a: { tool: string }) => a.tool === 'engine:transcript-mine');
        expect(mineRow.result_n).toBe(1);

        // The artifact content is now semantically recallable.
        const search = await (await request.get(
            `/api/memory/search?company_id=${companyId}&q=${encodeURIComponent('how do we verify payment webhooks')}`,
        )).json();
        const texts = (search.results || []).map((r: { text: string }) => r.text).join('\n');
        expect(texts).toContain('ZEBRA-ARTIFACT-9481');

        // KG facts: status=canceled + produced artifact, on the task entity.
        const facts = await (await request.get(
            `/api/memory/facts?company_id=${companyId}&entity=${encodeURIComponent(`task-${canceledTaskRef}`)}`,
        )).json();
        const byPredicate: Record<string, string[]> = {};
        for (const f of facts.facts || []) {
            (byPredicate[f.predicate] ||= []).push(f.object);
        }
        expect(byPredicate['status']).toContain('canceled');
        expect(byPredicate['produced']).toContain('artifact:webhook-notes.md');
        for (const objects of Object.values(byPredicate)) {
            for (const o of objects) expect(o).not.toMatch(/[#*`]/);
        }
    });

    test('paraphrase dedup: re-phrased fact rejected, different fact stored', async ({ request }) => {
        test.setTimeout(420_000);

        const finish = (id: string) => ({
            tool_call: { id, name: 'finish_task', arguments: { task_status: 'done', finish_status: 'Recorded.' } },
        });

        const t1 = await createTask(request, 'Fix the alpine build', [
            { tool_call: { id: 'd1', name: 'remember', arguments: { content: BLOCKER_FACT, kind: 'learning' } } },
            finish('d2'),
        ]);
        await waitForTaskStatus(request, t1, 'done', 180_000);

        // Wait until the first fact is actually queryable (drawer writes have
        // a short visibility lag) — otherwise task 2's duplicate check races
        // it and stores the paraphrase as a fresh fact.
        await expect.poll(async () => {
            const search = await (await request.get(
                `/api/memory/search?company_id=${companyId}&q=${encodeURIComponent('alpine build CGO linker')}`,
            )).json();
            return (search.results || []).some((r: { text: string }) => r.text.includes('CGO_ENABLED'));
        }, { timeout: 60_000, intervals: [2_000] }).toBe(true);

        const t2 = await createTask(request, 'Investigate CI compilation failures', [
            { tool_call: { id: 'd3', name: 'remember', arguments: { content: BLOCKER_PARAPHRASE, kind: 'learning' } } },
            { tool_call: { id: 'd4', name: 'remember', arguments: { content: DIFFERENT_FACT, kind: 'learning' } } },
            finish('d5'),
        ]);
        await waitForTaskStatus(request, t2, 'done', 180_000);

        // Introspect the tool results the engine fed back to the LLM.
        const introspect = await (await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const toolResults: string[] = [];
        for (const r of introspect.requests.filter((r: { path: string }) => r.path.includes('/chat/completions'))) {
            for (const m of (r.body?.messages || []) as { role: string; content?: string }[]) {
                if (m.role === 'tool' && typeof m.content === 'string') toolResults.push(m.content);
            }
        }
        // The paraphrase came back as "Already in memory", quoting the original.
        const dupReply = toolResults.find((c) => c.includes('Already in memory'));
        expect(dupReply, 'paraphrased remember must be rejected as a duplicate').toBeTruthy();
        expect(dupReply!).toContain('CGO_ENABLED');

        // Exactly one CGO fact drawer; the different fact was stored as its
        // own drawer. remember stores raw content (no prefix), so filter to
        // remember-written drawers by source_file basename instead: the API
        // reduces "tasks/<ref>/run-N" to "run-N", distinct from the
        // transcript miner's "transcript-N.jsonl" (which legitimately quotes
        // the fact text too) and artifact ingestion's "<filename>.<ext>".
        const drawers = await (await request.get(
            `/api/memory/drawers?company_id=${companyId}&wing=company&limit=100`,
        )).json();
        const factPreviews = (drawers.drawers || [])
            .filter((d: { metadata?: { source_file?: string } }) => /^run-\d+$/.test(d.metadata?.source_file || ''))
            .map((d: { content_preview: string }) => d.content_preview);
        expect(factPreviews.filter((p: string) => p.includes('CGO_ENABLED')).length).toBe(1);
        expect(factPreviews.filter((p: string) => p.includes('browser tools')).length).toBe(1);
    });

    test('protocol slimming: no write_diary tool, new remember guidance', async ({ request }) => {
        const introspect = await (await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const chatRequests = introspect.requests.filter((r: { path: string }) => r.path.includes('/chat/completions'));
        expect(chatRequests.length).toBeGreaterThan(0);

        for (const r of chatRequests) {
            const toolNames = ((r.body?.tools || []) as { function?: { name?: string } }[])
                .map((t) => t.function?.name);
            expect(toolNames).not.toContain('write_diary');
        }

        const lastReq = chatRequests[chatRequests.length - 1];
        const system = ((lastReq.body?.messages || []) as { role: string; content?: string }[])
            .find((m) => m.role === 'system')?.content ?? '';
        expect(system).toContain('durable learnings');
        expect(system).toContain('Knowledge-graph entities are named task-<ref>');
        expect(system).not.toContain('write_diary');
        expect(system).not.toContain('facts win');
    });

    test('teardown fired exactly once for the canceled run', async ({ request }) => {
        // Double-fire would show as 2+ auto-diary rows for the canceled run.
        const activity = await (await request.get(`/api/memory/activity?company_id=${companyId}&limit=200`)).json();
        const diaries = (activity.items || []).filter(
            (a: { tool: string; task_id?: number }) => a.tool === 'engine:auto-diary' && a.task_id === canceledTaskId,
        );
        expect(diaries.length).toBe(1);
    });
});

async function postJSON(request: APIRequestContext, url: string, data: object) {
    const res = await request.post(url, { data });
    if (!res.ok()) {
        throw new Error(`POST ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}

async function putJSON(request: APIRequestContext, url: string, data: object) {
    const res = await request.put(url, { data });
    if (!res.ok()) {
        throw new Error(`PUT ${url} failed (${res.status()}): ${await res.text()}`);
    }
    return res.json();
}
