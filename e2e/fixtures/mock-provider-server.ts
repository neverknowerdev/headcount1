import * as http from 'http';
import * as net from 'net';
import { AddressInfo } from 'net';

const MOCK_MODEL_ID = 'e2e-mock-model';
const TOOL_NAME = 'update_task_status';
const TOOL_CALL_ID = 'call_e2e_1';
const TOOL_ARGS = { status: 'in-review' };
const COMPLETION_TEXT = 'Task status has been updated. All done.';

interface ReceivedRequest {
    method: string;
    path: string;
    body: unknown;
    timestamp: number;
}

interface ChatCompletionRequest {
    model?: string;
    messages?: Array<{ role: string; content: string }>;
    stream?: boolean;
    [key: string]: unknown;
}

interface ChatChunkDelta {
    role?: 'assistant';
    content?: string;
    tool_calls?: Array<{
        index: number;
        id?: string;
        function?: {
            name?: string;
            arguments?: string;
        };
    }>;
}

interface ChatChunk {
    id: string;
    object: 'chat.completion.chunk';
    created: number;
    model: string;
    choices: Array<{
        index: number;
        delta: ChatChunkDelta;
        finish_reason: string | null;
    }>;
}

/**
 * A small HTTP server that emulates an OpenAI-compatible LLM provider for E2E
 * tests.
 *
 * Endpoints:
 *   - POST /v1/chat/completions   -> returns a tool call to `update_task_status`
 *                                   on the first request, then a text completion.
 *   - GET  /v1/models             -> returns one model so `TestProvider` succeeds.
 *   - GET  /__test/requests       -> returns the log of received requests (test introspection).
 *   - POST /__test/reset          -> clears the request log.
 *
 * The server prints a single line `MOCK_PROVIDER_READY <port>` to stdout once
 * it's listening. `global-setup.ts` parses that line.
 */
export async function startMockProviderServer(): Promise<{ baseUrl: string; port: number; stop: () => Promise<void> }> {
    const state = {
        received: [] as ReceivedRequest[],
        requestCount: 0,
        chatRequestCount: 0,
    };

    const server = http.createServer(async (req, res) => {
        const body = await parseRequestBody(req);

        state.requestCount++;
        state.received.push({ method: req.method || '', path: req.url || '', body, timestamp: Date.now() });

        if (handleTestRoutes(req, res, state)) return;
        if (handleModelsRoute(req, res)) return;
        if (handleChatCompletionsRoute(req, res, body, state)) return;

        res.writeHead(404, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'not_found', method: req.method, path: req.url }));
    });

    const port = await getFreePort();
    await new Promise<void>((resolve) => server.listen(port, '127.0.0.1', () => resolve()));
    const addr = server.address() as AddressInfo;
    const baseUrl = `http://127.0.0.1:${addr.port}`;

    // Print the ready line so global-setup can read it
    process.stdout.write(`MOCK_PROVIDER_READY ${addr.port} ${baseUrl}\n`);

    const stop = () => new Promise<void>((resolve) => server.close(() => resolve()));

    return { baseUrl, port: addr.port, stop };
}

async function parseRequestBody(req: http.IncomingMessage): Promise<unknown> {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk as Buffer);
    const rawBody = Buffer.concat(chunks).toString('utf8');
    if (!rawBody) return null;
    try {
        return JSON.parse(rawBody);
    } catch {
        return rawBody;
    }
}

function handleTestRoutes(
    req: http.IncomingMessage,
    res: http.ServerResponse,
    state: { received: ReceivedRequest[]; requestCount: number; chatRequestCount: number }
): boolean {
    if (req.url?.startsWith('/__test/requests') && req.method === 'GET') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ count: state.requestCount, requests: state.received }));
        return true;
    }
    if (req.url === '/__test/reset' && req.method === 'POST') {
        state.received.length = 0;
        state.requestCount = 0;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'ok' }));
        return true;
    }
    if (req.url === '/__test/health' && req.method === 'GET') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'ok' }));
        return true;
    }
    return false;
}

function handleModelsRoute(req: http.IncomingMessage, res: http.ServerResponse): boolean {
    if (req.url === '/v1/models' && req.method === 'GET') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
            object: 'list',
            data: [{ id: MOCK_MODEL_ID, object: 'model', owned_by: 'e2e' }],
        }));
        return true;
    }
    return false;
}

function handleChatCompletionsRoute(
    req: http.IncomingMessage,
    res: http.ServerResponse,
    body: unknown,
    state: { received: ReceivedRequest[]; requestCount: number; chatRequestCount: number }
): boolean {
    if (!req.url?.includes('/chat/completions') || req.method !== 'POST') {
        return false;
    }

    const request = body as ChatCompletionRequest;
    const wantsStream = request.stream === true;

    // Only count requests that include tool definitions — those are real agent
    // calls. The TestProvider endpoint sends a bare "Say hello" request without
    // tools to verify connectivity; that request should not advance the counter
    // so it doesn't consume the first-call slot.
    const hasTools = Array.isArray((request as any).tools) && (request as any).tools.length > 0;
    if (hasTools) {
        state.chatRequestCount++;
    }
    const isFirstCall = state.chatRequestCount === 1;

    if (wantsStream) {
        writeStreamingChatCompletion(res, isFirstCall);
    } else {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(buildChatCompletionResponse(isFirstCall)));
    }
    return true;
}

function buildChatCompletionResponse(withToolCall: boolean): object {
    const message = withToolCall
        ? {
            role: 'assistant' as const,
            content: 'I have analyzed the E2E task and completed it successfully.',
            tool_calls: [{
                id: TOOL_CALL_ID,
                type: 'function',
                function: {
                    name: TOOL_NAME,
                    arguments: JSON.stringify(TOOL_ARGS),
                },
            }],
        }
        : {
            role: 'assistant' as const,
            content: COMPLETION_TEXT,
        };

    return {
        id: `chatcmpl-e2e-${Date.now()}`,
        object: 'chat.completion',
        created: Math.floor(Date.now() / 1000),
        model: MOCK_MODEL_ID,
        choices: [{
            index: 0,
            message,
            finish_reason: withToolCall ? 'tool_calls' : 'stop',
        }],
        usage: { prompt_tokens: 10, completion_tokens: 20, total_tokens: 30 },
    };
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

function writeStreamingChatCompletion(res: http.ServerResponse, withToolCall: boolean): void {
    res.writeHead(200, {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        'Connection': 'keep-alive',
    });

    const chunk = createChunkFactory();

    res.write(formatSSE(chunk({ role: 'assistant' }, null)));

    if (withToolCall) {
        res.write(formatSSE(chunk({
            tool_calls: [{
                index: 0,
                id: TOOL_CALL_ID,
                function: { name: TOOL_NAME, arguments: '' },
            }],
        }, null)));

        res.write(formatSSE(chunk({
            tool_calls: [{
                index: 0,
                function: { arguments: JSON.stringify(TOOL_ARGS) },
            }],
        }, null)));

        res.write(formatSSE(chunk({}, 'tool_calls')));
    } else {
        res.write(formatSSE(chunk({ content: COMPLETION_TEXT }, null)));
        res.write(formatSSE(chunk({}, 'stop')));
    }

    res.write('data: [DONE]\n\n');
    res.end();
}

function createChunkFactory() {
    const id = `chatcmpl-e2e-${Date.now()}`;
    const created = Math.floor(Date.now() / 1000);

    return (delta: ChatChunkDelta, finishReason: string | null): ChatChunk => ({
        id,
        object: 'chat.completion.chunk',
        created,
        model: MOCK_MODEL_ID,
        choices: [{ index: 0, delta, finish_reason: finishReason }],
    });
}

function formatSSE(chunk: ChatChunk): string {
    return `data: ${JSON.stringify(chunk)}\n\n`;
}
