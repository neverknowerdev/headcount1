import * as http from 'http';
import * as crypto from 'crypto';
import { AddressInfo } from 'net';

interface ReceivedRequest {
    method: string;
    path: string;
    body: unknown;
    auth: string;
}

/**
 * A mock deploy-target server emulating the slices of the Vercel and GitHub
 * REST APIs that the deploysync layer talks to. One server plays both roles —
 * the Go server is pointed at it via VERCEL_API_URL and GITHUB_API_URL.
 *
 * Introspection:
 *   - GET  /__test/requests -> the full request log (method, path, body, auth)
 *   - POST /__test/reset    -> clears the log
 */
export async function startMockDeployServer(): Promise<{ baseUrl: string; stop: () => Promise<void> }> {
    const received: ReceivedRequest[] = [];

    // A real X25519 public key so the Go side's sealed-box encryption has a
    // valid recipient (decryptability is proven in the Go unit tests).
    const { publicKey } = crypto.generateKeyPairSync('x25519');
    const rawPub = (publicKey.export({ type: 'spki', format: 'der' }) as Buffer).subarray(-32);
    const ghPublicKeyB64 = rawPub.toString('base64');

    const server = http.createServer(async (req, res) => {
        const chunks: Buffer[] = [];
        for await (const c of req) chunks.push(c as Buffer);
        const raw = Buffer.concat(chunks).toString('utf8');
        let body: unknown = null;
        try { body = raw ? JSON.parse(raw) : null; } catch { body = raw; }

        const path = req.url || '';
        const record = () => received.push({
            method: req.method || '', path, body, auth: req.headers.authorization || '',
        });

        const json = (code: number, data: unknown) => {
            res.writeHead(code, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify(data));
        };

        // ── introspection ────────────────────────────────────────────────
        if (path.startsWith('/__test/requests')) return json(200, { requests: received });
        if (path.startsWith('/__test/reset')) { received.length = 0; return json(200, { ok: true }); }

        record();

        // ── Vercel ───────────────────────────────────────────────────────
        if (path.startsWith('/v10/projects/') && path.includes('/env') && req.method === 'POST') {
            return json(201, { created: [], failed: [] });
        }
        if (path.startsWith('/v10/projects/') && path.includes('/env') && req.method === 'GET') {
            return json(200, { envs: [{ id: 'env_row_1', key: 'DB_URL', target: ['production'] }] });
        }
        if (path.startsWith('/v10/projects')) {
            return json(200, { projects: [{ id: 'prj_mock1', name: 'mock-web' }, { id: 'prj_mock2', name: 'mock-api' }] });
        }
        if (path.includes('/custom-environments')) {
            return json(200, { environments: [{ id: 'env_custom1', slug: 'staging' }] });
        }
        if (path.startsWith('/v9/projects/') && path.includes('/env/') && req.method === 'DELETE') {
            return json(200, {});
        }

        // ── GitHub ───────────────────────────────────────────────────────
        if (path === '/user/repos' || path.startsWith('/user/repos?')) {
            return json(200, [{ full_name: 'acme/web' }, { full_name: 'acme/api' }]);
        }
        if (/^\/repos\/[^/]+\/[^/]+\/environments$/.test(path)) {
            return json(200, { environments: [{ name: 'production' }, { name: 'staging' }] });
        }
        if (path.endsWith('/secrets/public-key')) {
            return json(200, { key_id: 'mock-key-1', key: ghPublicKeyB64 });
        }
        if (path.includes('/secrets/') && req.method === 'PUT') {
            res.writeHead(201); return res.end();
        }
        if (path.endsWith('/variables') && req.method === 'POST') {
            res.writeHead(201); return res.end();
        }
        if (path.includes('/variables/') && (req.method === 'PATCH' || req.method === 'DELETE')) {
            res.writeHead(204); return res.end();
        }
        if (path.includes('/secrets/') && req.method === 'DELETE') {
            res.writeHead(204); return res.end();
        }

        json(404, { error: 'mock-deploy: unhandled', method: req.method, path });
    });

    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
    const addr = server.address() as AddressInfo;
    return {
        baseUrl: `http://127.0.0.1:${addr.port}`,
        stop: () => new Promise<void>((resolve) => server.close(() => resolve())),
    };
}
