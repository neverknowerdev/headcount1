import * as http from 'http';
import * as crypto from 'crypto';
import { AddressInfo } from 'net';

/**
 * A small HTTP server that emulates the Hindsight memory REST API for E2E
 * tests (tenant fixed to "default", storage in-memory, recall by naive
 * keyword overlap). Mirrors the endpoints the Go server's hindsight.Client
 * calls; see pkg/hindsight/client.go.
 *
 * Debug endpoints for test assertions:
 *   - GET  /__admin/dump   -> all banks and their memories
 *   - POST /__admin/reset  -> wipe all state
 */

interface Memory {
    id: string;
    text: string;
    context: string;
    type: string; // "world" | "experience"
    tags: string[];
    metadata: Record<string, string>;
    document_id: string;
    mentioned_at: string;
    occurred_start: string;
    state: string; // "active" | "invalidated"
}

interface RetainItem {
    content: string;
    timestamp?: string;
    context?: string;
    metadata?: Record<string, string>;
    document_id?: string;
    tags?: string[];
    update_mode?: string;
    observation_scopes?: string[][];
}

interface Directive {
    name: string;
    content: string;
}

interface MentalModel {
    id: string;
    name: string;
    source_query: string;
    tags: string[];
    max_tokens?: number;
    trigger?: { refresh_after_consolidation?: boolean };
    content: string;
    last_refreshed_at: string;
    history?: Array<{ previous_content: string; changed_at: string }>;
}

interface BankConfig {
    updates: Record<string, unknown>;
}

type Banks = Map<string, Memory[]>;
type BankConfigs = Map<string, Record<string, unknown>>;
type BankDirectives = Map<string, Directive[]>;
type BankMentalModels = Map<string, Map<string, MentalModel>>;
type LastRecalls = Map<string, Record<string, unknown>>;

/** Recomputes a mental model's content by concatenating the most recent
 * memories whose tags overlap the model's tags (empty tags = whole bank),
 * simulating Hindsight's refresh_after_consolidation trigger. */
function synthesize(mems: Memory[], tags: string[]): string {
    const matching = mems
        .filter((m) => m.state === 'active')
        .filter((m) => tags.length === 0 || m.tags.some((t) => tags.includes(t)))
        .slice()
        .sort((a, b) => (a.mentioned_at < b.mentioned_at ? 1 : -1))
        .slice(0, 5);
    return 'SYNTHESIS: ' + matching.map((m) => m.text).join(' | ');
}

/** Appends the model's current content to its refresh history (most recent
 * first), mirroring Hindsight's history endpoint used by the Memory UI to
 * show past runs of a mental model. */
function pushHistory(model: MentalModel): void {
    if (!model.history) model.history = [];
    model.history.unshift({ previous_content: model.content, changed_at: model.last_refreshed_at });
}

export async function startMockHindsightServer(): Promise<{ baseUrl: string; port: number; stop: () => Promise<void> }> {
    const banks: Banks = new Map();
    const bankConfigs: BankConfigs = new Map();
    const bankDirectives: BankDirectives = new Map();
    const bankModels: BankMentalModels = new Map();
    const lastRecalls: LastRecalls = new Map();
    // Records which tenant (the /v1/<tenant>/ path segment — an isolated
    // Postgres schema in real Hindsight) each bank was created under, so tests
    // can assert per-team isolation. Bank ids are globally unique (company-<id>),
    // so state stays keyed by bank; this just remembers the owning tenant.
    const bankTenants: Map<string, string> = new Map();

    const getBank = (bank: string): Memory[] => {
        if (!banks.has(bank)) banks.set(bank, []);
        return banks.get(bank)!;
    };
    const getDirectives = (bank: string): Directive[] => {
        if (!bankDirectives.has(bank)) bankDirectives.set(bank, []);
        return bankDirectives.get(bank)!;
    };
    const getModels = (bank: string): Map<string, MentalModel> => {
        if (!bankModels.has(bank)) bankModels.set(bank, new Map());
        return bankModels.get(bank)!;
    };
    // Refreshes every mental model in `bank` whose tags overlap `retainedTags`
    // (or has no tags — bank-wide models always refresh).
    const refreshModelsAfterRetain = (bank: string, retainedTags: string[]) => {
        const mems = getBank(bank);
        for (const model of getModels(bank).values()) {
            if (model.tags.length === 0 || model.tags.some((t) => retainedTags.includes(t))) {
                model.content = synthesize(mems, model.tags);
                model.last_refreshed_at = new Date().toISOString();
                pushHistory(model);
            }
        }
    };

    const server = http.createServer(async (req, res) => {
        try {
            const rawBody = await readBody(req);
            handle(req, res, rawBody, banks, getBank, bankConfigs, bankDirectives, getDirectives, bankModels, getModels, refreshModelsAfterRetain, lastRecalls, bankTenants);
        } catch (err: any) {
            json(res, 500, { error: err?.message || 'internal error' });
        }
    });

    // Listen on an OS-assigned free port directly (no probe-then-listen race).
    await new Promise<void>((resolve, reject) => {
        server.once('error', reject);
        server.listen(0, '127.0.0.1', () => {
            server.removeListener('error', reject);
            resolve();
        });
    });
    const addr = server.address() as AddressInfo;
    const baseUrl = `http://127.0.0.1:${addr.port}`;

    // Print the ready line so test output shows the port (same convention as
    // the mock provider fixture).
    process.stdout.write(`MOCK_HINDSIGHT_READY ${addr.port} ${baseUrl}\n`);

    const stop = () => new Promise<void>((resolve) => {
        // Drop keep-alive sockets (e.g. from the Go server's http.Client) so
        // close() resolves promptly instead of waiting out keepAliveTimeout.
        server.closeAllConnections();
        server.close(() => resolve());
    });
    return { baseUrl, port: addr.port, stop };
}

function handle(
    req: http.IncomingMessage,
    res: http.ServerResponse,
    rawBody: Buffer,
    banks: Banks,
    getBank: (b: string) => Memory[],
    bankConfigs: BankConfigs,
    bankDirectives: BankDirectives,
    getDirectives: (b: string) => Directive[],
    bankModels: BankMentalModels,
    getModels: (b: string) => Map<string, MentalModel>,
    refreshModelsAfterRetain: (bank: string, tags: string[]) => void,
    lastRecalls: LastRecalls,
    bankTenants: Map<string, string>,
): void {
    const method = req.method || 'GET';
    const u = new URL(req.url || '/', 'http://mock');
    const p = u.pathname;

    // ── admin / health ──────────────────────────────────────────────────
    if (method === 'GET' && p === '/health') return json(res, 200, { status: 'ok' });
    if (method === 'GET' && p === '/__admin/dump') {
        const dump: Record<string, Memory[]> = {};
        for (const [k, v] of banks.entries()) dump[k] = v;
        const configs: Record<string, Record<string, unknown>> = {};
        for (const [k, v] of bankConfigs.entries()) configs[k] = v;
        const directives: Record<string, Directive[]> = {};
        for (const k of banks.keys()) directives[k] = getDirectives(k);
        const mentalModels: Record<string, MentalModel[]> = {};
        for (const k of banks.keys()) mentalModels[k] = [...getModels(k).values()];
        const lastRecall: Record<string, Record<string, unknown>> = {};
        for (const [k, v] of lastRecalls.entries()) lastRecall[k] = v;
        const tenants: Record<string, string> = {};
        for (const [k, v] of bankTenants.entries()) tenants[k] = v;
        return json(res, 200, { banks: dump, configs, directives, mental_models: mentalModels, last_recall: lastRecall, tenants });
    }
    if (method === 'POST' && p === '/__admin/reset') {
        banks.clear();
        bankConfigs.clear();
        bankDirectives.clear();
        bankModels.clear();
        lastRecalls.clear();
        bankTenants.clear();
        return json(res, 200, { status: 'ok' });
    }

    // ── banks list (tenant-scoped) ──────────────────────────────────────
    // The tenant segment is a LITERAL in real hindsight-api (verified against
    // 0.6.1: every route is mounted under /v1/default/ and any other value
    // 404s, with or without a TenantExtension — its multi-tenancy is
    // credential-based, not path-based). The mock enforces that, so a change
    // that starts addressing /v1/<something-else>/ fails here instead of
    // silently passing the suite and 404-ing against a real backend.
    const listMatch = p.match(/^\/v1\/([^/]+)\/banks$/);
    if (method === 'GET' && listMatch) {
        if (decodeURIComponent(listMatch[1]) !== 'default') {
            return json(res, 404, { detail: 'Not Found' });
        }
        return json(res, 200, { banks: [...banks.keys()].map((b) => ({ bank_id: b })) });
    }

    const m = p.match(/^\/v1\/([^/]+)\/banks\/([^/]+)(\/.*)?$/);
    if (!m) return json(res, 404, { error: 'not found', path: p });
    const tenant = decodeURIComponent(m[1]);
    if (tenant !== 'default') return json(res, 404, { detail: 'Not Found' });
    const bank = decodeURIComponent(m[2]);
    const rest = m[3] || '';
    if (!bankTenants.has(bank)) bankTenants.set(bank, tenant);
    const body = parseJSON(rawBody);

    // ── create bank (hindsight.Client.CreateBank) ───────────────────────
    // Real Hindsight's PUT /v1/default/banks/{id} auto-creates the bank
    // profile (idempotently) so a later PATCH /config or POST /directives
    // has a row to act on; mirror that here so EnsureBank's create-then-
    // configure sequence works against the mock the same as the real API.
    if (method === 'PUT' && (rest === '' || rest === '/')) {
        getBank(bank);
        if (!bankConfigs.has(bank)) bankConfigs.set(bank, {});
        return json(res, 200, { bank_id: bank });
    }

    // ── delete bank (hindsight.Client.DeleteBank) ───────────────────────
    if (method === 'DELETE' && (rest === '' || rest === '/')) {
        banks.delete(bank);
        bankConfigs.delete(bank);
        bankDirectives.delete(bank);
        bankModels.delete(bank);
        lastRecalls.delete(bank);
        return json(res, 200, { status: 'ok' });
    }

    // ── retain ──────────────────────────────────────────────────────────
    if (method === 'POST' && rest === '/memories') {
        const mems = getBank(bank);
        const items: RetainItem[] = (body?.items as RetainItem[]) || [];
        const allTags = new Set<string>();
        for (const item of items) {
            if (item.document_id) {
                // upsert: drop previous memories of the same document
                const remaining = mems.filter((mm) => mm.document_id !== item.document_id);
                mems.length = 0;
                mems.push(...remaining);
            }
            const tags = item.tags || [];
            tags.forEach((t) => allTags.add(t));
            const isExperience = tags.some((t) => t.startsWith('agent:'));
            const ts = item.timestamp && item.timestamp !== 'unset' ? item.timestamp : new Date().toISOString();
            mems.push({
                id: crypto.randomUUID(),
                text: item.content || '',
                context: item.context || '',
                type: isExperience ? 'experience' : 'world',
                tags,
                metadata: item.metadata || {},
                document_id: item.document_id || '',
                mentioned_at: ts,
                occurred_start: ts,
                state: 'active',
            });
            // observation_scopes is accepted and otherwise ignored — the
            // mock doesn't model Hindsight's observation consolidation.
            // update_mode is likewise ignored: the mock always replaces by
            // document_id (real Hindsight's default); "append" is not modeled.
        }
        refreshModelsAfterRetain(bank, [...allTags]);
        return json(res, 200, { success: true });
    }

    // ── bank config ─────────────────────────────────────────────────────
    if (rest === '/config') {
        if (method === 'GET') {
            const config = bankConfigs.get(bank) || {};
            return json(res, 200, { bank_id: bank, config, overrides: config });
        }
        if (method === 'PATCH') {
            const existing = bankConfigs.get(bank) || {};
            const updates = (body as BankConfig)?.updates || {};
            bankConfigs.set(bank, { ...existing, ...updates });
            return json(res, 200, { status: 'ok' });
        }
    }

    // ── directives ──────────────────────────────────────────────────────
    if (method === 'GET' && rest === '/directives') {
        return json(res, 200, { items: getDirectives(bank) });
    }
    if (method === 'POST' && rest === '/directives') {
        const directives = getDirectives(bank);
        const name = body?.name as string;
        const content = body?.content as string;
        const existing = directives.find((d) => d.name === name);
        if (existing) existing.content = content;
        else directives.push({ name, content });
        return json(res, 200, { name, content });
    }

    // ── mental models ───────────────────────────────────────────────────
    if (method === 'POST' && rest === '/mental-models') {
        const models = getModels(bank);
        const id = (body?.id as string) || crypto.randomUUID();
        const model: MentalModel = {
            id,
            name: body?.name || id,
            source_query: body?.source_query || '',
            tags: body?.tags || [],
            max_tokens: body?.max_tokens,
            trigger: body?.trigger,
            content: 'Generating content...',
            last_refreshed_at: new Date().toISOString(),
            history: [],
        };
        models.set(id, model);
        // Seed content immediately from whatever memories already match —
        // real Hindsight generates async, but the mock has no async delay,
        // so there's no reason to leave it as a placeholder if data exists.
        model.content = synthesize(getBank(bank), model.tags);
        pushHistory(model);
        return json(res, 200, { mental_model_id: id, operation_id: 'op-1' });
    }
    if (method === 'GET' && rest === '/mental-models') {
        return json(res, 200, { items: [...getModels(bank).values()] });
    }
    const modelHistoryMatch = rest.match(/^\/mental-models\/([^/]+)\/history$/);
    if (method === 'GET' && modelHistoryMatch) {
        const id = decodeURIComponent(modelHistoryMatch[1]);
        const model = getModels(bank).get(id);
        if (!model) return json(res, 404, { error: 'mental model not found' });
        return json(res, 200, model.history || []); // bare array, like real Hindsight
    }
    const modelRefreshMatch = rest.match(/^\/mental-models\/([^/]+)\/refresh$/);
    if (method === 'POST' && modelRefreshMatch) {
        const id = decodeURIComponent(modelRefreshMatch[1]);
        const model = getModels(bank).get(id);
        if (!model) return json(res, 404, { error: 'mental model not found' });
        model.content = synthesize(getBank(bank), model.tags);
        model.last_refreshed_at = new Date().toISOString();
        pushHistory(model);
        return json(res, 200, { operation_id: 'op-1' });
    }
    const modelMatch = rest.match(/^\/mental-models\/([^/]+)$/);
    if (modelMatch) {
        const id = decodeURIComponent(modelMatch[1]);
        const model = getModels(bank).get(id);
        if (method === 'GET') {
            if (!model) return json(res, 404, { error: 'mental model not found' });
            return json(res, 200, model);
        }
        if (method === 'PATCH') {
            if (!model) return json(res, 404, { error: 'mental model not found' });
            if (typeof body?.name === 'string') model.name = body.name;
            if (typeof body?.source_query === 'string') model.source_query = body.source_query;
            if (Array.isArray(body?.tags)) model.tags = body.tags;
            if (typeof body?.max_tokens === 'number') model.max_tokens = body.max_tokens;
            if (body?.trigger) model.trigger = body.trigger;
            return json(res, 200, model);
        }
        if (method === 'DELETE') {
            getModels(bank).delete(id);
            return json(res, 200, { status: 'ok' });
        }
    }

    // ── tags ────────────────────────────────────────────────────────────
    if (method === 'GET' && rest === '/tags') {
        const counts = new Map<string, number>();
        for (const m of getBank(bank)) {
            if (m.state !== 'active') continue;
            for (const t of m.tags) counts.set(t, (counts.get(t) || 0) + 1);
        }
        const items = [...counts.entries()].map(([tag, count]) => ({ tag, count }));
        return json(res, 200, { items, total: items.length, limit: 100, offset: 0 });
    }

    // ── bank template export / import (config + mental models + directives) ──
    if (method === 'GET' && rest === '/export') {
        return json(res, 200, {
            version: '1',
            bank: bankConfigs.get(bank) || {},
            mental_models: [...getModels(bank).values()],
            directives: getDirectives(bank),
        });
    }
    if (method === 'POST' && rest === '/import') {
        const manifest = body || {};
        if (manifest.bank) bankConfigs.set(bank, { ...(bankConfigs.get(bank) || {}), ...manifest.bank });
        const models = getModels(bank);
        for (const m of (manifest.mental_models as MentalModel[]) || []) models.set(m.id, m);
        const directives = getDirectives(bank);
        for (const d of (manifest.directives as Directive[]) || []) {
            const existing = directives.find((x) => x.name === d.name);
            if (existing) existing.content = d.content;
            else directives.push(d);
        }
        return json(res, 200, {
            bank_id: bank, config_applied: !!manifest.bank,
            mental_models_created: [], mental_models_updated: [], directives_created: [], directives_updated: [],
            operation_ids: [], dry_run: false,
        });
    }

    // ── recall ──────────────────────────────────────────────────────────
    if (method === 'POST' && rest === '/memories/recall') {
        lastRecalls.set(bank, body || {});
        const results = recall(getBank(bank), body || {});
        return json(res, 200, { results });
    }

    // ── reflect ─────────────────────────────────────────────────────────
    if (method === 'POST' && rest === '/reflect') {
        const results = recall(getBank(bank), { query: body?.query || '', tags: body?.tags }).slice(0, 3);
        const text = 'Based on stored memories: ' + results.map((r) => r.text).join(' | ');
        return json(res, 200, { text });
    }

    // ── list memories ───────────────────────────────────────────────────
    if (method === 'GET' && rest === '/memories/list') {
        let mems = getBank(bank).slice();
        const q = u.searchParams.get('q');
        const type = u.searchParams.get('type');
        const docID = u.searchParams.get('document_id');
        // Default to active memories only (invalidated ones are curated away);
        // pass ?state=invalidated (or active) to filter explicitly.
        const state = u.searchParams.get('state') || 'active';
        if (q) mems = mems.filter((mm) => (mm.text + ' ' + mm.context).toLowerCase().includes(q.toLowerCase()));
        if (type) mems = mems.filter((mm) => mm.type === type);
        if (docID) mems = mems.filter((mm) => mm.document_id === docID);
        mems = mems.filter((mm) => mm.state === state);
        const total = mems.length;
        const offset = parseInt(u.searchParams.get('offset') || '0', 10) || 0;
        const limit = parseInt(u.searchParams.get('limit') || '100', 10) || 100;
        const items = mems.slice(offset, offset + limit).map((mm) => ({
            id: mm.id,
            text: mm.text,
            context: mm.context,
            date: mm.occurred_start,
            type: mm.type,
            document_id: mm.document_id,
            tags: mm.tags,
            state: mm.state,
        }));
        return json(res, 200, { items, total });
    }

    // ── single memory get/patch ─────────────────────────────────────────
    const memMatch = rest.match(/^\/memories\/([^/]+)$/);
    if (memMatch && (method === 'GET' || method === 'PATCH')) {
        const id = decodeURIComponent(memMatch[1]);
        const mem = getBank(bank).find((mm) => mm.id === id);
        if (!mem) return json(res, 404, { error: 'memory not found' });
        if (method === 'PATCH') {
            if (typeof body?.text === 'string') mem.text = body.text;
            if (typeof body?.context === 'string') mem.context = body.context;
            if (body?.state === 'invalidated' || body?.state === 'active') mem.state = body.state;
        }
        return json(res, 200, mem);
    }

    // ── graph / entities graph / stats / documents ──────────────────────
    if (method === 'GET' && rest === '/graph') {
        const q = (u.searchParams.get('q') || '').toLowerCase();
        let mems = getBank(bank);
        if (q) mems = mems.filter((mm) => (mm.text + ' ' + mm.context).toLowerCase().includes(q));
        // tags filter (all_strict semantics, like real Hindsight's default):
        // a memory must carry every requested tag.
        const tagFilter = u.searchParams.getAll('tags');
        if (tagFilter.length > 0) mems = mems.filter((mm) => tagFilter.every((t) => mm.tags.includes(t)));
        // Cytoscape-style { data: {...} } envelope, matching real Hindsight's
        // get_graph_data shape — the frontend normalizes this, so the mock
        // must return the same wire format or it can't catch a regression there.
        const nodes = mems.map((mm) => ({
            data: {
                id: mm.id,
                label: mm.text.length > 80 ? mm.text.slice(0, 80) + '…' : mm.text,
                type: mm.type,
            },
        }));
        return json(res, 200, { nodes, edges: [], table_rows: [], total_units: mems.length, limit: 1000 });
    }
    if (method === 'GET' && rest === '/entities/graph') {
        return json(res, 200, { nodes: [], edges: [] });
    }
    if (method === 'GET' && rest === '/stats') {
        // Invalidated (soft-deleted) memories don't count toward bank stats.
        return json(res, 200, { memory_units: getBank(bank).filter((mm) => mm.state === 'active').length });
    }
    if (method === 'GET' && rest === '/documents') {
        const docs = [...new Set(getBank(bank).map((mm) => mm.document_id).filter(Boolean))];
        return json(res, 200, { documents: docs.map((d) => ({ id: d })) });
    }

    // ── delete document (docID may contain slashes — match greedily) ────
    const docMatch = rest.match(/^\/documents\/(.+)$/);
    if (method === 'DELETE' && docMatch) {
        const docID = decodeURIComponent(docMatch[1]);
        const mems = getBank(bank);
        const remaining = mems.filter((mm) => mm.document_id !== docID);
        mems.length = 0;
        mems.push(...remaining);
        return json(res, 200, { success: true });
    }

    // ── document transfer (export / import) ─────────────────────────────
    if (method === 'GET' && rest === '/document-transfer') {
        const payload = Buffer.from(JSON.stringify({ bank_id: bank, memories: getBank(bank) }), 'utf8');
        res.writeHead(200, { 'Content-Type': 'application/zip', 'Content-Length': payload.length });
        res.end(payload);
        return;
    }
    if (method === 'POST' && rest === '/document-transfer') {
        const filePayload = extractMultipartFile(req, rawBody);
        const parsed = parseJSON(filePayload ?? rawBody);
        const incoming: Memory[] = (parsed?.memories as Memory[]) || [];
        const mems = getBank(bank);
        for (const inc of incoming) {
            // replace duplicates by document_id (or by id when no document_id)
            const remaining = mems.filter((mm) =>
                inc.document_id ? mm.document_id !== inc.document_id : mm.id !== inc.id);
            mems.length = 0;
            mems.push(...remaining);
            mems.push(inc);
        }
        return json(res, 202, { operation_id: 'op-1' });
    }

    json(res, 404, { error: 'not found', method, path: p });
}

/** Naive keyword-overlap recall over active memories. */
function recall(
    mems: Memory[],
    req: { query?: string; tags?: string[]; tags_match?: string; types?: string[] },
): Array<Record<string, unknown>> {
    const words = tokenize(req.query || '');
    const scored: Array<{ score: number; m: Memory }> = [];
    for (const m of mems) {
        if (m.state !== 'active') continue;
        if (req.types && req.types.length > 0 && !req.types.includes(m.type)) continue;
        if (req.tags && req.tags.length > 0) {
            // any_strict (and any): memory must share at least one tag.
            // tags_match "all"/"all_strict"/"exact" are NOT modeled — the Go
            // service only ever sends "any_strict" (pkg/hindsight/service.go).
            const shared = m.tags.some((t) => req.tags!.includes(t));
            if (!shared) continue;
        }
        const memWords = new Set(tokenize(m.text + ' ' + m.context));
        let score = 0;
        for (const w of words) if (memWords.has(w)) score++;
        if (score > 0) scored.push({ score, m });
    }
    scored.sort((a, b) => b.score - a.score);
    return scored.map(({ m }) => ({
        id: m.id,
        text: m.text,
        type: m.type,
        context: m.context,
        entities: [],
        occurred_start: m.occurred_start,
        document_id: m.document_id,
        metadata: m.metadata,
        tags: m.tags,
    }));
}

function tokenize(s: string): string[] {
    return s.toLowerCase().split(/[^a-z0-9]+/).filter((w) => w.length > 1);
}

/** Extract the first file part's content from a multipart/form-data body. */
function extractMultipartFile(req: http.IncomingMessage, rawBody: Buffer): Buffer | null {
    const ct = req.headers['content-type'] || '';
    const bMatch = ct.match(/boundary=(?:"([^"]+)"|([^;]+))/);
    if (!bMatch) return null;
    const boundary = '--' + (bMatch[1] || bMatch[2]);
    const bodyStr = rawBody.toString('binary');
    const parts = bodyStr.split(boundary);
    for (const part of parts) {
        const headerEnd = part.indexOf('\r\n\r\n');
        if (headerEnd === -1) continue;
        const headers = part.slice(0, headerEnd);
        if (!/content-disposition/i.test(headers)) continue;
        let content = part.slice(headerEnd + 4);
        if (content.endsWith('\r\n')) content = content.slice(0, -2);
        return Buffer.from(content, 'binary');
    }
    return null;
}

function json(res: http.ServerResponse, status: number, data: unknown): void {
    res.writeHead(status, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(data));
}

function parseJSON(buf: Buffer | null): any {
    if (!buf || buf.length === 0) return null;
    try { return JSON.parse(buf.toString('utf8')); } catch { return null; }
}

async function readBody(req: http.IncomingMessage): Promise<Buffer> {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk as Buffer);
    return Buffer.concat(chunks);
}
