import * as http from 'http';
import * as net from 'net';
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
}

type Banks = Map<string, Memory[]>;

export async function startMockHindsightServer(): Promise<{ baseUrl: string; port: number; stop: () => Promise<void> }> {
    const banks: Banks = new Map();

    const getBank = (bank: string): Memory[] => {
        if (!banks.has(bank)) banks.set(bank, []);
        return banks.get(bank)!;
    };

    const server = http.createServer(async (req, res) => {
        try {
            const rawBody = await readBody(req);
            handle(req, res, rawBody, banks, getBank);
        } catch (err: any) {
            json(res, 500, { error: err?.message || 'internal error' });
        }
    });

    const port = await getFreePort();
    await new Promise<void>((resolve) => server.listen(port, '127.0.0.1', () => resolve()));
    const addr = server.address() as AddressInfo;
    const baseUrl = `http://127.0.0.1:${addr.port}`;

    process.stdout.write(`MOCK_HINDSIGHT_READY ${addr.port} ${baseUrl}\n`);

    const stop = () => new Promise<void>((resolve) => server.close(() => resolve()));
    return { baseUrl, port: addr.port, stop };
}

function handle(
    req: http.IncomingMessage,
    res: http.ServerResponse,
    rawBody: Buffer,
    banks: Banks,
    getBank: (b: string) => Memory[],
): void {
    const method = req.method || 'GET';
    const u = new URL(req.url || '/', 'http://mock');
    const p = u.pathname;

    // ── admin / health ──────────────────────────────────────────────────
    if (method === 'GET' && p === '/health') return json(res, 200, { status: 'ok' });
    if (method === 'GET' && p === '/__admin/dump') {
        const dump: Record<string, Memory[]> = {};
        for (const [k, v] of banks.entries()) dump[k] = v;
        return json(res, 200, { banks: dump });
    }
    if (method === 'POST' && p === '/__admin/reset') {
        banks.clear();
        return json(res, 200, { status: 'ok' });
    }

    // ── banks list ──────────────────────────────────────────────────────
    if (method === 'GET' && p === '/v1/default/banks') {
        return json(res, 200, { banks: [...banks.keys()].map((b) => ({ bank_id: b })) });
    }

    const m = p.match(/^\/v1\/default\/banks\/([^/]+)(\/.*)?$/);
    if (!m) return json(res, 404, { error: 'not found', path: p });
    const bank = decodeURIComponent(m[1]);
    const rest = m[2] || '';
    const body = parseJSON(rawBody);

    // ── retain ──────────────────────────────────────────────────────────
    if (method === 'POST' && rest === '/memories') {
        const mems = getBank(bank);
        const items: RetainItem[] = (body?.items as RetainItem[]) || [];
        for (const item of items) {
            if (item.document_id) {
                // upsert: drop previous memories of the same document
                const remaining = mems.filter((mm) => mm.document_id !== item.document_id);
                mems.length = 0;
                mems.push(...remaining);
            }
            const tags = item.tags || [];
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
        }
        return json(res, 200, { success: true });
    }

    // ── recall ──────────────────────────────────────────────────────────
    if (method === 'POST' && rest === '/memories/recall') {
        const results = recall(getBank(bank), body || {});
        return json(res, 200, { results });
    }

    // ── reflect ─────────────────────────────────────────────────────────
    if (method === 'POST' && rest === '/reflect') {
        const results = recall(getBank(bank), { query: body?.query || '' }).slice(0, 3);
        const text = 'Based on stored memories: ' + results.map((r) => r.text).join(' | ');
        return json(res, 200, { text });
    }

    // ── list memories ───────────────────────────────────────────────────
    if (method === 'GET' && rest === '/memories/list') {
        let mems = getBank(bank).slice();
        const q = u.searchParams.get('q');
        const type = u.searchParams.get('type');
        const docID = u.searchParams.get('document_id');
        const state = u.searchParams.get('state');
        if (q) mems = mems.filter((mm) => (mm.text + ' ' + mm.context).toLowerCase().includes(q.toLowerCase()));
        if (type) mems = mems.filter((mm) => mm.type === type);
        if (docID) mems = mems.filter((mm) => mm.document_id === docID);
        if (state) mems = mems.filter((mm) => mm.state === state);
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
        const nodes = mems.map((mm) => ({
            id: mm.id,
            label: mm.text.length > 80 ? mm.text.slice(0, 80) + '…' : mm.text,
            type: mm.type,
        }));
        return json(res, 200, { nodes, edges: [], table_rows: [], total_units: mems.length, limit: 1000 });
    }
    if (method === 'GET' && rest === '/entities/graph') {
        return json(res, 200, { nodes: [], edges: [] });
    }
    if (method === 'GET' && rest === '/stats') {
        return json(res, 200, { memory_units: getBank(bank).length });
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
            // any_strict (and any): memory must share at least one tag
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

function getFreePort(): Promise<number> {
    return new Promise((resolve, reject) => {
        const srv = net.createServer();
        srv.unref();
        srv.on('error', reject);
        srv.listen(0, '127.0.0.1', () => {
            const addr = srv.address() as AddressInfo;
            const port = addr.port;
            srv.close(() => resolve(port));
        });
    });
}
