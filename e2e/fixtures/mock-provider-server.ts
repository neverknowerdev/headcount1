import * as http from 'http';
import * as net from 'net';
import { AddressInfo } from 'net';

const MOCK_MODEL_ID = 'e2e-mock-model';
const CEO_MODEL_ID = 'e2e-ceo-model';
const AGENT_A_MODEL_ID = 'e2e-agent-a-model';
const AGENT_B_MODEL_ID = 'e2e-agent-b-model';
const TOOL_NAME = 'finish_task';
const TOOL_CALL_ID = 'call_e2e_1';
const TOOL_ARGS = { task_status: 'in-review', finish_status: 'E2E task completed and ready for review.' };
const ORCHESTRATOR_TOOL_NAME = 'run_new_session';
const ORCHESTRATOR_TOOL_CALL_ID = 'call_e2e_orchestrator_1';
const ORCHESTRATOR_TOOL_ARGS = {
    agent_name: 'E2E Agent',
    prompt: 'Complete the assigned task and finish the task when the implementation is ready for review.',
};
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
    /** Emit a tool call. */
    tool_call?: ScenarioToolCall;
    /** Emit several tool calls in the same assistant turn. */
    tool_calls?: ScenarioToolCall[];
    /** Emit a plain text completion. */
    text?: string;
}

interface ScenarioState {
    entries: ScenarioEntry[];
    index: number;
    inboundReadyFor?: Set<string>;
}

const GENERIC_FORWARDING_INBOUND = '__forwarding-inbound__';

interface ScenarioTemplate {
    entries: ScenarioEntry[];
    inboundEntries?: ScenarioEntry[];
    forkEntries?: ScenarioEntry[];
    retryEntries?: ScenarioEntry[];
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
 *   - POST /v1/chat/completions   -> returns a tool call to `finish_task`
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
        scenario: null as ScenarioState | null,
        scenarios: new Map<string, ScenarioState>(),
        scenarioTemplates: new Map<string, ScenarioTemplate>(),
        // Hold support (used by the auto-update drain/resume test): while active,
        // every /chat/completions request blocks after being logged and before
        // responding, until POST /__test/release resolves it. This lets a test
        // catch an agent run provably mid-turn (blocked on its LLM call) so it
        // can SIGTERM the server and exercise graceful drain deterministically.
        holdActive: false,
        holdModelFilter: null as Set<string> | null,
        holdWaiters: [] as Array<() => void>,
        completionsReceived: 0,
        orchestratorStartedTasks: new Set<string>(),
        shutdown: null as (() => Promise<void>) | null,
    };

    const sockets = new Set<net.Socket>();

    const server = http.createServer(async (req, res) => {
        const body = await parseRequestBody(req);

        state.requestCount++;
        state.received.push({ method: req.method || '', path: req.url || '', body, timestamp: Date.now() });

        if (handleTestRoutes(req, res, body, state)) return;
        if (handleModelsRoute(req, res)) return;

        // Block a chat-completions call while a hold is active. Logged above
        // first, so /__test/requests reflects that the call arrived even while
        // it's held.
        const isCompletions = (req.url?.includes('/chat/completions') ?? false) && req.method === 'POST';
        if (isCompletions) {
            state.completionsReceived++;
            const model = String((body as ChatCompletionRequest | null)?.model || '');
            if (state.holdActive && (!state.holdModelFilter || state.holdModelFilter.has(model))) {
                await new Promise<void>((resolve) => state.holdWaiters.push(resolve));
            }
        }

        if (handleChatCompletionsRoute(req, res, body, state)) return;

        res.writeHead(404, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'not_found', method: req.method, path: req.url }));
    });

    server.on('connection', (socket) => {
        sockets.add(socket);
        socket.once('close', () => sockets.delete(socket));
    });
    await new Promise<void>((resolve, reject) => {
        server.once('error', reject);
        server.listen(0, '127.0.0.1', () => resolve());
    });
    const addr = server.address() as AddressInfo;
    const baseUrl = `http://127.0.0.1:${addr.port}`;

    // Print the ready line so global-setup can read it
    process.stdout.write(`MOCK_PROVIDER_READY ${addr.port} ${baseUrl}\n`);

    const stop = async (): Promise<void> => {
        // Release requests held by the drain/resume scenario before closing.
        state.holdActive = false;
        state.holdModelFilter = null;
        for (const resolve of state.holdWaiters.splice(0)) resolve();
        if (typeof server.closeAllConnections === 'function') server.closeAllConnections();
        for (const socket of sockets) socket.destroy();
        if (!server.listening) return;
        await new Promise<void>((resolve) => {
            const timer = setTimeout(resolve, 2_000);
            server.close(() => { clearTimeout(timer); resolve(); });
        });
    };
    state.shutdown = stop;

    return { baseUrl, port: addr.port, stop };
}

async function parseRequestBody(req: http.IncomingMessage): Promise<unknown> {
    // Some drain/resume tests deliberately hold a completed request open while
    // the server drains (longer than the normal body-read budget). Keep the
    // socket alive for that bounded test window so the provider does not turn
    // one logical completion into several retries and consume multiple scenario
    // entries.
    req.setTimeout(120_000, () => req.destroy(new Error('request body timeout')));
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

interface MockState {
    received: ReceivedRequest[];
    requestCount: number;
    scenario: ScenarioState | null;
    scenarios: Map<string, ScenarioState>;
    scenarioTemplates: Map<string, ScenarioTemplate>;
    holdActive: boolean;
    holdModelFilter: Set<string> | null;
    holdWaiters: Array<() => void>;
    completionsReceived: number;
    orchestratorStartedTasks: Set<string>;
    shutdown: (() => Promise<void>) | null;
}

function handleTestRoutes(
    req: http.IncomingMessage,
    res: http.ServerResponse,
    body: unknown,
    state: MockState
): boolean {
    if (req.url?.startsWith('/__test/requests') && req.method === 'GET') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
            count: state.requestCount,
            completionsReceived: state.completionsReceived,
            requests: state.received,
        }));
        return true;
    }
    // Activate hold: subsequent chat-completions calls block until released.
    if (req.url === '/__test/hold' && req.method === 'POST') {
        state.holdActive = true;
        state.holdModelFilter = null;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'ok', hold: true }));
        return true;
    }
    if (req.url === '/__test/hold-worker' && req.method === 'POST') {
        state.holdActive = true;
        state.holdModelFilter = new Set([MOCK_MODEL_ID]);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'ok', hold: true, model: MOCK_MODEL_ID }));
        return true;
    }
    // Release: deactivate hold and unblock every currently-waiting call.
    if (req.url === '/__test/release' && req.method === 'POST') {
        state.holdActive = false;
        state.holdModelFilter = null;
        const waiters = state.holdWaiters.splice(0);
        for (const resolve of waiters) resolve();
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'ok', released: waiters.length }));
        return true;
    }
    if (req.url === '/__test/reset' && req.method === 'POST') {
        state.received.length = 0;
        state.requestCount = 0;
        state.completionsReceived = 0;
        state.orchestratorStartedTasks.clear();
        state.scenario = null;
        state.scenarios.clear();
        state.scenarioTemplates.clear();
        state.holdActive = false;
        state.holdModelFilter = null;
        const waiters = state.holdWaiters.splice(0);
        for (const resolve of waiters) resolve();
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'ok' }));
        return true;
    }
    if (req.url === '/__test/shutdown' && req.method === 'POST') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'stopping' }));
        setImmediate(() => { void state.shutdown?.(); });
        return true;
    }
    if (req.url === '/__test/set-scenario' && req.method === 'POST') {
        const data = body as { entries: ScenarioEntry[]; inbound_entries?: ScenarioEntry[]; fork_entries?: ScenarioEntry[]; retry_entries?: ScenarioEntry[]; model?: string };
        const next = { entries: data.entries ?? [], index: 0, inboundReadyFor: new Set<string>() };
        if (data.model) {
            state.scenarios.set(data.model, next);
            state.scenarioTemplates.set(data.model, {
                entries: data.entries ?? [], inboundEntries: data.inbound_entries,
                forkEntries: data.fork_entries, retryEntries: data.retry_entries,
            });
        }
        else state.scenario = next;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'ok', count: next.entries.length, model: data.model || null }));
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
            data: [
                { id: MOCK_MODEL_ID, object: 'model', owned_by: 'e2e' },
                { id: 'e2e-orchestrator-model', object: 'model', owned_by: 'e2e' },
                { id: CEO_MODEL_ID, object: 'model', owned_by: 'e2e' },
                { id: AGENT_A_MODEL_ID, object: 'model', owned_by: 'e2e' },
                { id: AGENT_B_MODEL_ID, object: 'model', owned_by: 'e2e' },
                { id: 'e2e-cto-model', object: 'model', owned_by: 'e2e' },
                { id: 'e2e-coder-model', object: 'model', owned_by: 'e2e' },
                { id: 'e2e-qa-model', object: 'model', owned_by: 'e2e' },
                { id: 'e2e-helper-model', object: 'model', owned_by: 'e2e' },
            ],
        }));
        return true;
    }
    return false;
}

function handleChatCompletionsRoute(
    req: http.IncomingMessage,
    res: http.ServerResponse,
    body: unknown,
    state: MockState
): boolean {
    if (!req.url?.includes('/chat/completions') || req.method !== 'POST') {
        return false;
    }

    const request = body as ChatCompletionRequest;
    const wantsStream = request.stream === true;
    const requestTools = Array.isArray((request as any).tools) ? (request as any).tools : [];
    const answerTool = requestTools.find((tool: any) => tool?.function?.name === 'answer_message');

    // Scenario mode: consume entries in order, fall back to "Done." after exhaustion.
    const model = String(request.model || '');
    const sessionID = runtimeSessionID(request);
    let modelScenario = state.scenarios.get(model);
    const template = state.scenarioTemplates.get(model);
    if (template && sessionID) {
        const sessionKey = `${model}#${sessionID}`;
        modelScenario = state.scenarios.get(sessionKey);
        if (!modelScenario) {
            const content = (Array.isArray(request.messages) ? request.messages : [])
                .map((message) => String(message.content ?? '')).join('\n');
            const entries = content.includes('Fork replay') && template.forkEntries ? template.forkEntries
                : content.toLowerCase().includes('re-verify') && template.retryEntries ? template.retryEntries
                    : requestHasIncoming(request) && template.inboundEntries ? template.inboundEntries : template.entries;
            modelScenario = { entries, index: 0, inboundReadyFor: new Set<string>() };
            state.scenarios.set(sessionKey, modelScenario);
        }
    }
    const scenario = modelScenario || state.scenario;
    if (scenario) {
        const sc = scenario;
        const candidate = sc.index < sc.entries.length ? sc.entries[sc.index] : null;
        const hasIncomingAnswer = answerTool && requestHasIncoming(request);
        const inboundGate = scenarioEntryInboundGate(candidate);
        // An inbound event can arrive while a long orchestration turn is
        // already consuming scripted management actions. Answer it at the
        // next request that includes the event instead of relying on the
        // scripted action index to line up with delivery timing.
        if (hasIncomingAnswer && !scenarioEntryCanProcessIncoming(candidate)) {
            // The owner may receive a worker question before the scripted
            // inspection turn that precedes its forwarding action. Remember
            // that the question was answered so the later route cannot wait
            // forever for an event that was already consumed durably.
            sc.inboundReadyFor?.add(inboundGate ?? GENERIC_FORWARDING_INBOUND);
            const message = latestIncomingContent(request) ?? '';
            const messageID = pendingIncomingMessageID(request);
            const answer: ScenarioEntry = { tool_call: { id: 'auto-answer-inbound', name: 'answer_message', arguments: {
                message_id: messageID ?? 1,
                answer: 'Use the existing event ordering and preserve the current API contract.',
            } } };
            if (wantsStream) writeStreamingScenarioEntry(res, answer, request);
            else { res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(buildScenarioResponse(answer, request))); }
            return true;
        }
        // Keep the orchestrator from entering a blocking outbound route before
        // the worker question it is meant to forward has reached its inbox.
        // This removes scheduler timing from the E2E scenario while still
        // exercising the real durable answer and routing paths.
        const waitingForForwardedQuestion = inboundGate
            && !sc.inboundReadyFor?.has(inboundGate)
            && !sc.inboundReadyFor?.has(GENERIC_FORWARDING_INBOUND)
            && !requestHasIncoming(request);
        // A worker may finish its owner-wait at the same moment the
        // orchestrator sends the next message. Keep an answer entry queued if
        // that message has not reached this turn yet; the engine's forced
        // follow-up will re-enter BeforeTurn and receive it without consuming
        // the scripted answer prematurely.
        const waitingForIncoming = candidate && scenarioEntryNeedsIncomingAnswer(candidate) && !requestHasIncoming(request);
        const entry = waitingForIncoming || waitingForForwardedQuestion
            ? requestTools.some((tool: any) => tool?.function?.name === 'report_status')
                ? {
                    // Keep the session inside the agent loop while the sender
                    // queues the message. A plain text response would end the
                    // run before BeforeTurn can observe the routed event.
                    tool_call: { id: 'wait-for-routed-message', name: 'report_status', arguments: { status: 'Waiting for the next routed message.' } },
                }
                : {
                    // The orchestrator has no report_status tool; use its
                    // registered read-only management tool for the same wait.
                    tool_call: { id: 'wait-for-routed-message', name: 'get_session_list', arguments: {} },
                }
            : candidate;
        if (!waitingForIncoming && candidate) {
            if (inboundGate) {
                sc.inboundReadyFor?.delete(inboundGate);
                sc.inboundReadyFor?.delete(GENERIC_FORWARDING_INBOUND);
            }
            sc.index++;
        }

        if (wantsStream) {
            writeStreamingScenarioEntry(res, entry, request);
        } else {
            res.writeHead(200, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify(buildScenarioResponse(entry, request)));
        }
        return true;
    }

    // Default mode: task orchestrators launch the configured worker first;
    // ordinary agent sessions finish the task directly. This keeps the basic
    // onboarding flow representative of the mandatory orchestrator runtime.
    const messages = Array.isArray((request as any).messages) ? (request as any).messages : [];
    const hasToolResult = messages.some((m: any) => m.role === 'tool');
    const isOrchestrator = requestTools.some((tool: any) => tool?.function?.name === ORCHESTRATOR_TOOL_NAME);
    if (answerTool && !isOrchestrator) {
        const match = String(latestIncomingContent(request) ?? '').match(/(?:message_id|"id")\s*[=:]\s*(\d+)/);
        const answer = { tool_call: { id: 'call-e2e-answer', name: 'answer_message', arguments: {
            message_id: match ? Number(match[1]) : 1,
            answer: 'Use the existing event ordering and preserve the current API contract.',
        } } };
        if (wantsStream) writeStreamingScenarioEntry(res, answer, request);
        else { res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(buildScenarioResponse(answer, request))); }
        return true;
    }
    const systemContext = messages.find((message: any) => message.role === 'system')?.content ?? '';
    const taskMatch = String(systemContext).match(/Task:.*?\(id:\s*(\d+)\)/);
    const taskKey = taskMatch?.[1] ?? 'unknown-task';
    const isFirstCall = !hasToolResult && (!isOrchestrator || !state.orchestratorStartedTasks.has(taskKey));
    if (isOrchestrator && isFirstCall) state.orchestratorStartedTasks.add(taskKey);

    if (wantsStream) {
        writeStreamingChatCompletion(res, isFirstCall, isOrchestrator);
    } else {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(buildChatCompletionResponse(isFirstCall, isOrchestrator)));
    }
    return true;
}

function buildScenarioResponse(entry: ScenarioEntry | null, request?: ChatCompletionRequest): object {
    if (!entry || entry.text !== undefined) {
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
    const toolCalls = (entry.tool_calls ?? (entry.tool_call ? [entry.tool_call] : [])).map((toolCall) => resolveScenarioToolCall(toolCall, request));
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
                tool_calls: toolCalls.map((tc) => ({
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

function writeStreamingScenarioEntry(res: http.ServerResponse, entry: ScenarioEntry | null, request?: ChatCompletionRequest): void {
    res.writeHead(200, {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        'Connection': 'keep-alive',
    });

    const chunk = createChunkFactory();
    res.write(formatSSE(chunk({ role: 'assistant' }, null)));

    if (!entry || entry.text !== undefined) {
        const text = entry?.text ?? 'Done.';
        res.write(formatSSE(chunk({ content: text }, null)));
        res.write(formatSSE(chunk({}, 'stop')));
    } else {
        const toolCalls = (entry.tool_calls ?? (entry.tool_call ? [entry.tool_call] : [])).map((toolCall) => resolveScenarioToolCall(toolCall, request));
        for (const [index, tc] of toolCalls.entries()) {
            res.write(formatSSE(chunk({
                tool_calls: [{ index, id: tc.id, function: { name: tc.name, arguments: '' } }],
            }, null)));
            res.write(formatSSE(chunk({
                tool_calls: [{ index, function: { arguments: JSON.stringify(tc.arguments) } }],
            }, null)));
        }
        res.write(formatSSE(chunk({}, 'tool_calls')));
    }

    res.write('data: [DONE]\n\n');
    res.end();
}

function scenarioEntryNeedsIncomingAnswer(entry: ScenarioEntry | null): boolean {
    return (entry?.tool_calls ?? (entry?.tool_call ? [entry.tool_call] : [])).some((toolCall) => toolCall.name === 'answer_message' && Number(toolCall.arguments.message_id) === 0);
}

function scenarioEntryCanProcessIncoming(entry: ScenarioEntry | null): boolean {
    // Outbound routing is not an answer to the event currently delivered to
    // this session. Treating send_message_to_session as an inbound handler can
    // deadlock the deterministic scenario: the orchestrator sends another
    // message while a worker's ask_task_owner call is still waiting for its
    // correlated answer. Only answer_message consumes the pending event.
    return (entry?.tool_calls ?? (entry?.tool_call ? [entry.tool_call] : [])).some((toolCall) =>
        toolCall.name === 'answer_message');
}

function scenarioEntryInboundGate(entry: ScenarioEntry | null): string | null {
    const id = (entry?.tool_calls ?? (entry?.tool_call ? [entry.tool_call] : []))[0]?.id;
    if (id === 'route-coder-to-cto' || id === 'route-qa-issue-to-coder') return id;
    return null;
}

function requestHasIncoming(request: ChatCompletionRequest): boolean {
    return latestIncomingContent(request) !== null;
}

function latestIncomingContent(request: ChatCompletionRequest): string | null {
    const messages = Array.isArray(request.messages) ? request.messages : [];
    for (let index = messages.length - 1; index >= 0; index--) {
        const message = messages[index];
        // Only the trailing user block can represent the event injected for
        // this provider turn. Once an answer/tool result is appended, older
        // Incoming text is conversation history, not a newly pending event.
        if (message.role !== 'user') break;
        if (String(message.content).includes('Incoming')) {
            return String(message.content);
        }
    }
    return null;
}

function runtimeSessionID(request: ChatCompletionRequest): string | null {
    const system = (Array.isArray(request.messages) ? request.messages : [])
        .find((message) => message.role === 'system')?.content ?? '';
    return String(system).match(/Runtime session ID:\s*(\d+)/)?.[1] ?? null;
}

function pendingIncomingMessageID(request: ChatCompletionRequest): number | null {
    const incoming = latestIncomingContent(request);
    if (!incoming) return null;
    const direct = incoming.match(/message_id=(\d+)/);
    if (direct) return Number(direct[1]);
    const routedMessage = incoming.match(/\{"id":(\d+)[^{}]*"event_type":"session_message"/);
    return routedMessage ? Number(routedMessage[1]) : null;
}

/** Resolve IDs that are assigned by the database during an E2E run. A zero in
 * a scenario means "the relevant ID from this conversation", so the test
 * describes message direction without hard-coding database sequence values. */
function resolveScenarioToolCall(toolCall: ScenarioToolCall, request?: ChatCompletionRequest): ScenarioToolCall {
    if (!request) return toolCall;
    const messages = Array.isArray(request.messages) ? request.messages : [];
    const messageID = pendingIncomingMessageID(request);
    const toolResults = messages
        .filter((item) => item.role === 'tool')
        .map((item) => String(item.content))
        .join('\n');
    const consultationID = toolResults.match(/consultation_run_id["=:]+(\d+)/)?.[1];
    const toolResultSessionIDs = [...toolResults.matchAll(/(?:new|replacement) child session (\d+)/g)].map((match) => match[1]);
    // Orchestrator activations intentionally start with a fresh model history.
    // Later turns therefore no longer contain the earlier run_new_session tool
    // result; recover child IDs from the authoritative session snapshot too.
    const snapshotSessionIDs = [...String(messages.map((item) => item.content).join('\n')).matchAll(/\{\s*"id"\s*:\s*(\d+)\s*,\s*"name"\s*:/g)].map((match) => match[1]);
    const sessionIDs = [...new Set([...toolResultSessionIDs, ...snapshotSessionIDs])];
    const snapshot = String(messages.map((item) => item.content).join('\n'));
    const sessionMatches = [...snapshot.matchAll(/\{"id":(\d+),.*?"agent_name":"([^"]+)"/g)];
    const wantedRole = toolCall.id.includes('cto') ? 'cto'
        : toolCall.id.includes('coder') ? 'coder'
            : toolCall.id.includes('qa') ? 'qa'
                : '';
    const matchingSessions = wantedRole
        ? sessionMatches.filter((match) => match[2].toLowerCase().includes(wantedRole))
        : [];
    // Nested helper workers inherit their parent agent's name. For a CTO
    // route, select the first (parent) CTO session rather than the most recent
    // nested helper; Coder/QA recovery routes intentionally select the latest
    // matching agent session.
    const matchingSession = wantedRole === 'cto'
        ? matchingSessions[0]?.[1]
        : matchingSessions.at(-1)?.[1];
    const sessionID = matchingSession ?? (toolCall.id.includes('-b') ? sessionIDs.at(-1) : sessionIDs[0]);
    const args = { ...toolCall.arguments };
    if (toolCall.name === 'answer_message' && Number(args.message_id) === 0) {
        args.message_id = messageID ?? 1;
    }
    if (toolCall.name === 'get_session' && Number(args.session_id) === 0) {
        args.session_id = consultationID ? Number(consultationID) : (sessionID ? Number(sessionID) : 1);
    }
    if (toolCall.name === 'send_message_to_session' && Number(args.session_id) === 0) {
        args.session_id = sessionID ? Number(sessionID) : 1;
    }
    if (toolCall.name === 'fork_session' && Number(args.session_id) === 0) {
        args.session_id = sessionID ? Number(sessionID) : 1;
    }
    if (toolCall.name === 'fork_session' && Number(args.fork_message_id) === 0) {
        const statusMessageIDs = [...snapshot.matchAll(/"message_id":(\d+)/g)].map((match) => Number(match[1]));
        args.fork_message_id = statusMessageIDs.at(-1) ?? 1;
    }
    return { ...toolCall, arguments: args };
}

function buildChatCompletionResponse(withToolCall: boolean, isOrchestrator: boolean): object {
    const useOrchestratorTool = withToolCall && isOrchestrator;
    const message = withToolCall
        ? {
            role: 'assistant' as const,
            content: useOrchestratorTool
                ? 'I have selected the implementation worker for this task.'
                : 'I have analyzed the E2E task and completed it successfully.',
            tool_calls: [{
                id: useOrchestratorTool ? ORCHESTRATOR_TOOL_CALL_ID : TOOL_CALL_ID,
                type: 'function',
                function: {
                    name: useOrchestratorTool ? ORCHESTRATOR_TOOL_NAME : TOOL_NAME,
                    arguments: JSON.stringify(useOrchestratorTool ? ORCHESTRATOR_TOOL_ARGS : TOOL_ARGS),
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

function writeStreamingChatCompletion(res: http.ServerResponse, withToolCall: boolean, isOrchestrator: boolean): void {
    res.writeHead(200, {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        'Connection': 'keep-alive',
    });

    const chunk = createChunkFactory();

    res.write(formatSSE(chunk({ role: 'assistant' }, null)));

    if (withToolCall) {
        const useOrchestratorTool = isOrchestrator;
        const toolName = useOrchestratorTool ? ORCHESTRATOR_TOOL_NAME : TOOL_NAME;
        const toolCallID = useOrchestratorTool ? ORCHESTRATOR_TOOL_CALL_ID : TOOL_CALL_ID;
        const toolArgs = useOrchestratorTool ? ORCHESTRATOR_TOOL_ARGS : TOOL_ARGS;
        res.write(formatSSE(chunk({
            tool_calls: [{
                index: 0,
                id: toolCallID,
                function: { name: toolName, arguments: '' },
            }],
        }, null)));

        res.write(formatSSE(chunk({
            tool_calls: [{
                index: 0,
                function: { arguments: JSON.stringify(toolArgs) },
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
