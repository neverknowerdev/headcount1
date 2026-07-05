import { test, expect, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { loadE2EEnv } from '../helpers/env';
import { waitForTaskStatus } from '../helpers/wait-for';

const env = loadE2EEnv();

// Two unique facts planted by "work" in tasks 1 and 2, recalled in task 3.
const REGION_FACT =
    'We decided to deploy to the Tokyo region (ap-northeast-1) because latency to users in Japan matters most.';
const JWT_FACT =
    'Learned during auth work: our JWT access tokens expire after 15 minutes; refresh tokens last 30 days.';

/**
 * MemPalace memory integration, end-to-end through the real engine with a
 * scripted mock LLM:
 *
 *   Task 1 (emulated work): agent calls remember(<region decision>) and finishes.
 *   Task 2 (emulated work): agent calls remember(<JWT learning>) and finishes.
 *   Task 3 (recall):        agent calls recall_memory("which cloud region…")
 *                           — the tool result fed back to the LLM must contain
 *                           the verbatim fact stored in task 1.
 *
 * Plus: the Memory API (search/taxonomy/drawers/activity/agents) and the
 * Memory UI page against the palace those runs produced.
 *
 * The mempalace runtime is installed by the app's own setup script into
 * PAPERCLIP_VENV_DIR (cached across e2e runs in e2e/.venv-cache), so the first
 * run of this spec may spend a while waiting for /api/memory/status → ready.
 */
test.describe.serial('MemPalace memory integration', () => {
    let companyId: number;
    let agentId: number;
    let sprintId: number;
    let providerId: number;

    const paperclipBase = path.join(env.E2E_PAPERCLIP_HOME, '.paperclip2');

    const cleanFilesystem = () => {
        for (const subDir of ['data/mem-co', 'companies/mem-co', 'workspace/mem-co', 'memory']) {
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

    test('memory layer becomes ready for a company', async ({ request }) => {
        // Waiting for readiness can include the setup script's pip install of
        // mempalace on a cold cache — allow plenty of time.
        test.setTimeout(360_000);

        const provider = await postJSON(request, '/api/providers', {
            name: 'e2e-mem-mock',
            base_url: env.E2E_MOCK_PROVIDER_URL,
            api_key: 'test-key',
            provider_type: 'openai',
            default_model: 'e2e-mock-model',
            supported_models: 'e2e-mock-model',
        });
        providerId = provider.id;

        const company = await postJSON(request, '/api/companies', {
            name: 'Memory Co', short_name: 'mem-co', color: '#4f46e5',
        });
        companyId = company.id;

        const agent = await postJSON(request, '/api/agents', {
            company_id: companyId,
            name: 'Orchestrator',
            system_prompt: 'You orchestrate.',
            model: 'e2e-mock-model',
            provider_id: provider.id,
        });
        agentId = agent.id;

        const sprint = await postJSON(request, '/api/sprints', {
            company_id: companyId, name: 'Memory Sprint',
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

    /**
     * finish_task now nudges once per run when the agent hasn't called
     * remember() yet ("store durable facts, then finish_task again" — see
     * engine/aicli/tools/finish_task_execution.go). A real LLM adapts to
     * that on the fly; this fixed mock script can't, so every scripted
     * finish_task call gets an identical retry spliced in right after it.
     * Unused (mock entries are pulled lazily) when the first call already
     * finishes; consumed as the agent's next scripted move when it doesn't.
     */
    function withFinishRetries(entries: object[]): object[] {
        const out: object[] = [];
        for (const entry of entries) {
            out.push(entry);
            const tc = (entry as any).tool_call;
            if (tc?.name === 'finish_task') {
                out.push({ tool_call: { id: `${tc.id}-retry`, name: 'finish_task', arguments: tc.arguments } });
            }
        }
        return out;
    }

    /** Runs one task whose scripted agent performs the given tool calls, then finishes. */
    async function runTask(
        request: APIRequestContext,
        title: string,
        entries: object[],
    ): Promise<number> {
        entries = withFinishRetries(entries);
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
            description: `${title} (e2e memory scenario)`,
            task_type: 'implement',
        });
        await putJSON(request, `/api/tasks/${task.id}`, { status: 'to-do' });
        await waitForTaskStatus(request, task.id, 'done', 180_000);
        return task.id;
    }

    test('tasks 1 & 2: agents store memories while working', async ({ request }) => {
        test.setTimeout(420_000);

        // Task 1: plant the region decision. The first palace write also warms
        // up the embedding model, hence the generous per-task timeout.
        await runTask(request, 'Set up deployment infrastructure', [
            { tool_call: { id: 'm1', name: 'remember', arguments: { content: REGION_FACT, kind: 'decision' } } },
            { tool_call: { id: 'm2', name: 'finish_task', arguments: { task_status: 'done', finish_status: 'Infra configured; decision recorded.' } } },
        ]);

        // Task 2: plant the JWT learning.
        await runTask(request, 'Implement authentication', [
            { tool_call: { id: 'm3', name: 'remember', arguments: { content: JWT_FACT, kind: 'learning' } } },
            { tool_call: { id: 'm4', name: 'finish_task', arguments: { task_status: 'done', finish_status: 'Auth implemented; learning recorded.' } } },
        ]);

        // Both facts must now be verbatim drawers in the company wing.
        const drawersRes = await request.get(`/api/memory/drawers?company_id=${companyId}&wing=company&limit=50`);
        expect(drawersRes.ok()).toBeTruthy();
        const drawers = (await drawersRes.json()).drawers || [];
        const previews = drawers.map((d: { content_preview: string }) => d.content_preview).join('\n');
        expect(previews).toContain('ap-northeast-1');
        expect(previews).toContain('15 minutes');
    });

    test('task 3: recall_memory brings back the fact from task 1', async ({ request }) => {
        test.setTimeout(300_000);

        await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/reset`, { method: 'POST' });
        // reset clears the scenario too, so re-arm it inside runTask.
        await runTask(request, 'Prepare the launch runbook', [
            { tool_call: { id: 'r1', name: 'recall_memory', arguments: { query: 'which cloud region do we deploy to and why' } } },
            { tool_call: { id: 'r2', name: 'finish_task', arguments: { task_status: 'done', finish_status: 'Runbook prepared using recalled decisions.' } } },
        ]);

        // Introspect what the engine actually sent to the LLM: the request
        // following the recall_memory tool call must carry a role:"tool"
        // message whose content contains the verbatim fact from task 1.
        const introspect = await (await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/requests`)).json();
        const chatRequests = introspect.requests.filter((r: { path: string }) => r.path.includes('/chat/completions'));
        const toolResults: string[] = [];
        for (const r of chatRequests) {
            const messages = (r.body?.messages || []) as { role: string; content?: string }[];
            for (const m of messages) {
                if (m.role === 'tool' && typeof m.content === 'string') toolResults.push(m.content);
            }
        }
        const recallResult = toolResults.find((c) => c.includes('ap-northeast-1'));
        expect(recallResult, 'recall_memory tool result fed back to the LLM must contain the fact stored in task 1').toBeTruthy();
        // The recall result is a mempalace search payload — sanity-check shape.
        expect(recallResult!).toContain('"results"');

        // And it must NOT have come from the task's own conversation: the fact
        // string never appeared in task 3's seed (different task, no comments).
        const task3Seeds = chatRequests
            .map((r: any) => (r.body?.messages || []).filter((m: any) => m.role === 'user').map((m: any) => m.content).join('\n'))
            .join('\n');
        expect(task3Seeds).not.toContain('ap-northeast-1');
    });

    test('memory API: search, taxonomy, activity and agent stats reflect the runs', async ({ request }) => {
        // Semantic search finds the JWT learning.
        const searchRes = await request.get(
            `/api/memory/search?company_id=${companyId}&q=${encodeURIComponent('how long do auth tokens last')}`,
        );
        expect(searchRes.ok()).toBeTruthy();
        const search = await searchRes.json();
        const texts = (search.results || []).map((r: { text: string }) => r.text).join('\n');
        expect(texts).toContain('15 minutes');

        // Taxonomy contains the company wing where the facts were filed.
        const taxonomy = await (await request.get(`/api/memory/taxonomy?company_id=${companyId}`)).json();
        expect(Object.keys(taxonomy.taxonomy || {})).toContain('company');

        // Activity feed attributes reads/writes to the agent (config name CEO).
        const activity = await (await request.get(`/api/memory/activity?company_id=${companyId}&limit=100`)).json();
        const tools = activity.items.map((a: { tool: string }) => a.tool);
        expect(tools).toContain('remember');
        expect(tools).toContain('recall_memory');
        const rememberRow = activity.items.find((a: { tool: string }) => a.tool === 'remember');
        expect(rememberRow.kind).toBe('write');
        expect(rememberRow.agent_name).toBeTruthy();

        // Per-agent stats aggregate those rows.
        const agents = await (await request.get(`/api/memory/agents?company_id=${companyId}`)).json();
        expect(agents.length).toBeGreaterThan(0);
        const stat = agents.find((a: { agent_name: string }) => a.agent_name === rememberRow.agent_name);
        expect(stat).toBeTruthy();
        expect(stat.writes).toBeGreaterThan(0);

        // The status endpoint reports a live palace with drawers.
        const status = await (await request.get(`/api/memory/status?company_id=${companyId}`)).json();
        expect(status.ready).toBe(true);
        expect(status.palace.total_drawers).toBeGreaterThanOrEqual(2);
    });

    test('memory UI: page renders explorer, search and activity against the palace', async ({ page }) => {
        await page.goto('/companies/mem-co/memory');

        // Header + stat tiles.
        await expect(page.getByRole('heading', { name: 'Memory' })).toBeVisible();
        await expect(page.getByText('Drawers', { exact: true }).first()).toBeVisible({ timeout: 15_000 });

        // Explorer: company wing visible; clicking it lists drawers.
        await expect(page.getByRole('button', { name: 'company', exact: true })).toBeVisible();
        await page.getByRole('button', { name: 'company', exact: true }).click();
        await expect(page.getByText('ap-northeast-1').first()).toBeVisible();

        // Search tab round-trip.
        await page.getByRole('button', { name: 'Search' }).first().click();
        await page.getByPlaceholder(/Semantic search/).fill('jwt token expiry');
        await page.getByRole('button', { name: 'Search', exact: true }).last().click();
        await expect(page.getByText('15 minutes').first()).toBeVisible({ timeout: 30_000 });

        // Activity tab shows the recorded operations.
        await page.getByRole('button', { name: 'Activity' }).click();
        await expect(page.getByText('remember').first()).toBeVisible();
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
