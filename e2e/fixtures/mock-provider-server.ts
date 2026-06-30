import * as http from 'http';
import * as net from 'net';
import { AddressInfo } from 'net';

const MOCK_MODEL_ID = 'e2e-mock-model';
const COMPLETION_TEXT = 'Task is now in review. All done.';

interface ReceivedRequest {
    method: string;
    path: string;
    body: unknown;
    timestamp: number;
}

/**
 * A scenario is an ordered sequence of responses the mock server returns
 * instead of its default logic. Each entry is either a tool call or a text
 * completion. The server works through entries one-by-one on each POST to
 * /v1/chat/completions, cycling to a text "Done." after the last entry.
 */
export interface ScenarioToolCall {
    id: string;
    name: string;
    arguments: Record<string, unknown>;
}

export interface ScenarioEntry {
    /** Emit a single tool call. */
    tool_call?: ScenarioToolCall;
    /** Emit multiple tool calls in the same assistant turn (e.g. batched ask_question). */
    tool_calls?: ScenarioToolCall[];
    /** Emit a plain text completion. */
    text?: string;
}

interface ScenarioState {
    entries: ScenarioEntry[];
    index: number;
}

interface ChatCompletionRequest {
    model?: string;
    messages?: Array<{ role: string; content: string }>;
    tools?: Array<{ type: string; function?: { name?: string } }>;
    stream?: boolean;
    [key: string]: unknown;
}

const FINISH_REFINEMENT_TOOL = 'finish_refinement';
const DEFAULT_REFINEMENT_ARGS = {
    detailed_description: 'E2E default-mode task.',
    specifications: 'Implement as described.',
    acceptance_criteria: 'Works correctly.',
    test_cases: '[]',
};

/** True when the request's tool list includes finish_refinement, identifying
 *  the caller as the SmartPlanner orchestrator (refinement stage). */
function isSmartPlannerRequest(request: ChatCompletionRequest): boolean {
    return (request.tools ?? []).some((t) => t.function?.name === FINISH_REFINEMENT_TOOL);
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
 *   - POST /v1/chat/completions   -> in default mode, pipeline-aware: SmartPlanner
 *                                   calls (tools include finish_refinement) get a
 *                                   finish_refinement tool call; all other calls
 *                                   (Coder, Tester, researchers) get a text reply.
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
        scenario: null as ScenarioState | null,
    };

    const server = http.createServer(async (req, res) => {
        const body = await parseRequestBody(req);

        state.requestCount++;
        state.received.push({ method: req.method || '', path: req.url || '', body, timestamp: Date.now() });

        if (handleTestRoutes(req, res, body, state)) return;
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
    body: unknown,
    state: { received: ReceivedRequest[]; requestCount: number; scenario: ScenarioState | null }
): boolean {
    if (req.url?.startsWith('/__test/requests') && req.method === 'GET') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ count: state.requestCount, requests: state.received }));
        return true;
    }
    if (req.url === '/__test/reset' && req.method === 'POST') {
        state.received.length = 0;
        state.requestCount = 0;
        state.scenario = null;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'ok' }));
        return true;
    }
    if (req.url === '/__test/set-scenario' && req.method === 'POST') {
        const data = body as { entries: ScenarioEntry[] };
        state.scenario = { entries: data.entries ?? [], index: 0 };
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'ok', count: state.scenario.entries.length }));
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
    state: { received: ReceivedRequest[]; requestCount: number; scenario: ScenarioState | null }
): boolean {
    if (!req.url?.includes('/chat/completions') || req.method !== 'POST') {
        return false;
    }

    const request = body as ChatCompletionRequest;
    const wantsStream = request.stream === true;

    // Scenario mode: consume entries in order, fall back to "Done." after exhaustion.
    if (state.scenario) {
        const sc = state.scenario;
        const entry = sc.index < sc.entries.length ? sc.entries[sc.index++] : null;

        if (wantsStream) {
            writeStreamingScenarioEntry(res, entry);
        } else {
            res.writeHead(200, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify(buildScenarioResponse(entry)));
        }
        return true;
    }

    // Default mode (AskMode pipeline-aware, no scenario configured):
    //  - SmartPlanner calls (tools include finish_refinement) get a finish_refinement
    //    tool call immediately, completing the refinement stage in one turn.
    //  - All other calls (Coder, Tester, researchers, escalation one-shots) get a
    //    plain text reply, which ends that stage's agent loop successfully.
    if (isSmartPlannerRequest(request)) {
        if (wantsStream) {
            writeStreamingScenarioEntry(res, { tool_call: { id: 'default_fr', name: FINISH_REFINEMENT_TOOL, arguments: DEFAULT_REFINEMENT_ARGS } });
        } else {
            res.writeHead(200, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify(buildScenarioResponse({ tool_call: { id: 'default_fr', name: FINISH_REFINEMENT_TOOL, arguments: DEFAULT_REFINEMENT_ARGS } })));
        }
        return true;
    }

    if (wantsStream) {
        writeStreamingChatCompletion(res);
    } else {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(buildChatCompletionResponse()));
    }
    return true;
}

function buildScenarioResponse(entry: ScenarioEntry | null): object {
    if (!entry || (entry.text !== undefined && !entry.tool_call && !entry.tool_calls)) {
        const text = entry?.text ?? 'Done.';
        return {
            id: `chatcmpl-sc-${Date.now()}`,
            object: 'chat.completion',
            created: Math.floor(Date.now() / 1000),
            model: MOCK_MODEL_ID,
            choices: [{
                index: 0,
                message: { role: 'assistant' as const, content: text },
                finish_reason: 'stop',
            }],
            usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 },
        };
    }
    const tcs = entry.tool_calls ?? (entry.tool_call ? [entry.tool_call] : []);
    return {
        id: `chatcmpl-sc-${Date.now()}`,
        object: 'chat.completion',
        created: Math.floor(Date.now() / 1000),
        model: MOCK_MODEL_ID,
        choices: [{
            index: 0,
            message: {
                role: 'assistant' as const,
                content: null,
                tool_calls: tcs.map((tc) => ({
                    id: tc.id,
                    type: 'function',
                    function: { name: tc.name, arguments: JSON.stringify(tc.arguments) },
                })),
            },
            finish_reason: 'tool_calls',
        }],
        usage: { prompt_tokens: 10, completion_tokens: 20, total_tokens: 30 },
    };
}

function writeStreamingScenarioEntry(res: http.ServerResponse, entry: ScenarioEntry | null): void {
    res.writeHead(200, {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        'Connection': 'keep-alive',
    });

    const chunk = createChunkFactory();
    res.write(formatSSE(chunk({ role: 'assistant' }, null)));

    const tcs = entry?.tool_calls ?? (entry?.tool_call ? [entry.tool_call] : []);
    if (tcs.length === 0) {
        const text = entry?.text ?? 'Done.';
        res.write(formatSSE(chunk({ content: text }, null)));
        res.write(formatSSE(chunk({}, 'stop')));
    } else {
        tcs.forEach((tc, index) => {
            res.write(formatSSE(chunk({
                tool_calls: [{ index, id: tc.id, function: { name: tc.name, arguments: '' } }],
            }, null)));
            res.write(formatSSE(chunk({
                tool_calls: [{ index, function: { arguments: JSON.stringify(tc.arguments) } }],
            }, null)));
        });
        res.write(formatSSE(chunk({}, 'tool_calls')));
    }

    res.write('data: [DONE]\n\n');
    res.end();
}

function buildChatCompletionResponse(): object {
    return {
        id: `chatcmpl-e2e-${Date.now()}`,
        object: 'chat.completion',
        created: Math.floor(Date.now() / 1000),
        model: MOCK_MODEL_ID,
        choices: [{
            index: 0,
            message: { role: 'assistant' as const, content: COMPLETION_TEXT },
            finish_reason: 'stop',
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

function writeStreamingChatCompletion(res: http.ServerResponse): void {
    res.writeHead(200, {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        'Connection': 'keep-alive',
    });

    const chunk = createChunkFactory();

    res.write(formatSSE(chunk({ role: 'assistant' }, null)));
    res.write(formatSSE(chunk({ content: COMPLETION_TEXT }, null)));
    res.write(formatSSE(chunk({}, 'stop')));

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
