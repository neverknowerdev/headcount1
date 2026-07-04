import { test, expect, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';

const env = loadE2EEnv();

/**
 * T3 (spec §4): the paraphrase dedup gauntlet, against the real palace and
 * real MiniLM embeddings — no mocked similarity scores.
 *
 * One fact, 5 paraphrases (reordered clauses, synonyms, a prefix-noise
 * marker, trailing task-ref-like noise, casual phrasing) that must ALL be
 * rejected as duplicates, plus 2 genuinely different facts that share
 * vocabulary with it, which must both be stored. This pins the dedup
 * threshold (`rememberDupThreshold` in engine/aicli/tools/mempalace.go) —
 * if it fails, the failure direction (paraphrase stored twice vs different
 * fact swallowed) says which way to tune it.
 *
 * Also proves the raw-content storage design end to end: the "already in
 * memory" replies must quote the fact verbatim with no "[closet] [kind]"
 * tag, and the stored drawer must be the bare fact — the prefix was removed
 * because it measurably compressed dedup's similarity range (see the
 * threshold's doc comment).
 */
const ORIGINAL_FACT =
    'Build needs CGO_ENABLED=0 on alpine images, otherwise the linker fails with missing libc symbols';

const PARAPHRASES = [
    'The linker fails with missing libc symbols on alpine unless CGO_ENABLED=0 is set for the build',
    'IMPORTANT: on alpine the compilation breaks with libc linker errors unless CGO_ENABLED is set to 0',
    'DEC-77 note: alpine builds need CGO_ENABLED=0, or you get linker errors about missing libc symbols',
    'Compiling for alpine requires disabling cgo (CGO_ENABLED=0); otherwise linking fails due to absent libc symbols',
    "Remember: CGO must be disabled (CGO_ENABLED=0) when targeting alpine, else the linker can't find libc symbols",
];

const DIFFERENT_FACTS = [
    'Agents lack browser tools, so web UI testing has to be delegated to the QA browser worker',
    'The staging database credentials rotate every 90 days and must be re-synced with the deploy secrets',
];

test.describe.serial('MemPalace paraphrase dedup gauntlet (T3)', () => {
    let companyId: number;
    let agentId: number;
    let sprintId: number;
    let providerId: number;

    const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');

    const cleanFilesystem = () => {
        for (const subDir of ['data/gt-co', 'companies/gt-co', 'workspace/gt-co', 'memory/gt-co']) {
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
            name: 'e2e-gauntlet-mock',
            base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key',
            provider_type: 'openai',
            default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model',
        });
        providerId = provider.id;

        const company = await postJSON(request, '/api/companies', {
            name: 'Gauntlet Co', short_name: 'gt-co', color: '#f97316',
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
            company_id: companyId, name: 'Gauntlet Sprint',
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

    test('gauntlet: 5 paraphrases rejected, 2 different facts stored', async ({ request }) => {
        test.setTimeout(300_000);

        const rememberCall = (id: string, content: string) => ({
            tool_call: { id, name: 'remember', arguments: { content, kind: 'learning' } },
        });

        const entries = [
            rememberCall('g0', ORIGINAL_FACT),
            ...PARAPHRASES.map((p, i) => rememberCall(`gp${i}`, p)),
            ...DIFFERENT_FACTS.map((d, i) => rememberCall(`gd${i}`, d)),
            { tool_call: { id: 'gf', name: 'finish_task', arguments: { task_status: 'done', finish_status: 'Gauntlet run recorded.' } } },
        ];

        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/set-scenario`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ entries }),
        });
        const task = await postJSON(request, '/api/tasks', {
            company_id: companyId,
            sprint_id: sprintId,
            agent_id: agentId,
            title: 'Dedup gauntlet run',
            description: 'Store one fact, 5 paraphrases and 2 different facts via remember.',
            task_type: 'implement',
        });
        await putJSON(request, `/api/tasks/${task.id}`, { status: 'to-do' });
        await waitForTaskStatus(request, task.id, 'done', 180_000);

        // Introspect the tool result for each remember call. Every request
        // resends the FULL prior history, so the same tool_call_id's result
        // appears in every subsequent request too — key by tool_call_id and
        // keep one entry per id, not one per occurrence.
        const introspect = await (await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const rememberCallIds = new Set(
            entries.filter((e: any) => e.tool_call?.name === 'remember').map((e: any) => e.tool_call.id),
        );
        const resultById = new Map<string, string>();
        for (const r of introspect.requests.filter((r: { path: string }) => r.path.includes('/chat/completions'))) {
            const messages = (r.body?.messages || []) as { role: string; content?: string; tool_call_id?: string }[];
            for (const m of messages) {
                if (m.role === 'tool' && typeof m.content === 'string' && m.tool_call_id && rememberCallIds.has(m.tool_call_id)) {
                    resultById.set(m.tool_call_id, m.content);
                }
            }
        }
        expect(resultById.size, 'every remember call must have produced a tool result').toBe(rememberCallIds.size);

        const rememberResults = Array.from(resultById.values());
        const duplicateReplies = rememberResults.filter((c) => c.includes('Already in memory'));
        const storedReplies = rememberResults.filter((c) => !c.includes('Already in memory'));

        expect(duplicateReplies.length, `all 5 paraphrases must be rejected as duplicates (got ${duplicateReplies.length}/5)`).toBe(5);
        expect(storedReplies.length, `original + 2 different facts must be stored (got ${storedReplies.length}/3)`).toBe(3);

        // Duplicate replies quote the original fact VERBATIM — no "[closet]
        // [kind]" tag — proving raw-content storage/dedup landed correctly.
        for (const reply of duplicateReplies) {
            expect(reply).toContain('CGO_ENABLED');
            expect(reply).not.toMatch(/\[task-.*\]\s*\[learning\]/);
        }

        // Exactly one CGO drawer stored in the palace (the original; every
        // paraphrase deduped against it), plus the 2 different facts. Filter
        // to remember-written drawers by source_file basename — the API
        // reduces source_file to Path(...).name, so a remember drawer's
        // "tasks/<ref>/run-N" becomes just "run-N" (matched below), distinct
        // from the transcript miner's "transcript-N.jsonl" and artifact
        // ingestion's "<filename>.<ext>". The transcript miner also files
        // the run's conversation (which legitimately quotes the fact text)
        // under its own source_file, and remember content no longer carries
        // a distinguishing prefix — hence this metadata-based filter.
        const drawers = await (await request.get(
            `/api/memory/drawers?company_id=${companyId}&wing=company&limit=100`,
        )).json();
        const factDrawers = (drawers.drawers || []).filter((d: { metadata?: { source_file?: string } }) =>
            /^run-\d+$/.test(d.metadata?.source_file || ''),
        );
        const previews = factDrawers.map((d: { content_preview: string }) => d.content_preview);
        const cgoDrawers = previews.filter((p: string) => p.includes('CGO_ENABLED'));
        expect(cgoDrawers.length, 'exactly one CGO drawer — all paraphrases deduped').toBe(1);
        // The stored drawer is the bare fact, not tagged.
        expect(cgoDrawers[0]).not.toMatch(/\[task-.*\]\s*\[learning\]/);

        expect(previews.some((p: string) => p.includes('browser tools'))).toBe(true);
        expect(previews.some((p: string) => p.includes('database credentials') || p.includes('rotate every 90 days'))).toBe(true);
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
