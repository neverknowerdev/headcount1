import * as http from 'http';
import * as net from 'net';
import { AddressInfo } from 'net';

/** Models the mock lists. Tests point the smart and the cheap tier at different ones. */
export const SMART_MODEL = 'e2e-smart-model';
export const CHEAP_MODEL = 'e2e-cheap-model';
export const MOCK_MODELS = [SMART_MODEL, CHEAP_MODEL, 'e2e-mock-model', 'e2e-other-model'];

/** Every completion reports the same usage, so a test can predict totals from call counts. */
export const USAGE_PER_CALL = { prompt_tokens: 100, completion_tokens: 10, total_tokens: 110 };

export interface ScenarioToolCall {
    name: string;
    arguments: Record<string, unknown>;
}

/** One scripted reply. */
export interface ScenarioEntry {
    /** Answer with one tool call. */
    tool_call?: ScenarioToolCall;
    /** Answer with several tool calls in one assistant turn. */
    tool_calls?: ScenarioToolCall[];
    /** Answer with plain text. */
    text?: string;
    /** Answer with an HTTP error of this status instead. */
    status?: number;
    /** Do not answer until POST /__test/release. */
    hold?: boolean;
}

/** Which requests a rule answers. Every field given must match. */
export interface ScenarioMatch {
    /** The request offers this tool. `finish_work` means an executor session; a
     *  phase's finishing tool (`finish_refinement`, `finish_verification`, ...)
     *  means a smart step of that phase; `create_tasks` alone means planning. */
    tool?: string;
    /** The workflow phase, derived from the tools on offer: refine, design,
     *  test_plan, plan, adjust, verify, executor, checkpoint or text. */
    phase?: string;
    /** This text appears somewhere in the request's messages. */
    contains?: string;
    /** The requested model. */
    model?: string;
}

/**
 * A rule answers the requests it matches with its replies, in order, one per
 * request. Once its replies are used up the rule no longer matches, unless
 * `repeat` is set, in which case its last reply answers every further match.
 * A request no rule matches gets the autopilot's answer.
 */
export interface ScenarioRule {
    match: ScenarioMatch;
    replies: ScenarioEntry[];
    repeat?: boolean;
}

interface RuleState extends ScenarioRule {
    used: number;
}

interface ChatMessage {
    role: string;
    content?: unknown;
    tool_calls?: unknown;
}

interface ChatCompletionRequest {
    model?: string;
    messages?: ChatMessage[];
    tools?: { function?: { name?: string; parameters?: { required?: string[] } } }[];
    stream?: boolean;
}

export interface ReceivedRequest {
    method: string;
    path: string;
    body: unknown;
    timestamp: number;
    /** For a completion: the phase it was recognised as and the reply it got. */
    phase?: string;
    reply?: ScenarioEntry;
}

interface MockState {
    received: ReceivedRequest[];
    completionsReceived: number;
    completionsAnswered: number;
    rules: RuleState[];
    holdAll: ScenarioMatch | null;
    holdWaiters: Array<() => void>;
    held: number;
    shutdown: (() => Promise<void>) | null;
}

const oneDecision = (title: string) => [{ title, decision: title, rationale: 'Scripted by the E2E mock provider.' }];

function toolNames(request: ChatCompletionRequest): string[] {
    return (request.tools || []).map((tool) => tool.function?.name || '').filter(Boolean);
}

/** The workflow phase a request belongs to, read off the tools it offers. */
export function phaseOf(request: ChatCompletionRequest): string {
    const tools = toolNames(request);
    const has = (name: string) => tools.includes(name);
    if (has('finish_work')) return 'executor';
    if (tools.length === 1 && has('checkpoint')) return 'checkpoint';
    if (has('finish_refinement')) return 'refine';
    if (has('finish_design')) return 'design';
    if (has('finish_test_plan')) return 'test_plan';
    if (has('finish_adjustment')) return 'adjust';
    if (has('finish_verification')) return 'verify';
    if (has('create_tasks')) return 'plan';
    return 'text';
}

function messageText(request: ChatCompletionRequest): string {
    return (request.messages || [])
        .map((message) => (typeof message.content === 'string' ? message.content : JSON.stringify(message.content ?? '')))
        .join('\n');
}

function matches(match: ScenarioMatch, request: ChatCompletionRequest): boolean {
    if (match.model && request.model !== match.model) return false;
    if (match.tool && !toolNames(request).includes(match.tool)) return false;
    if (match.phase && phaseOf(request) !== match.phase) return false;
    if (match.contains && !messageText(request).includes(match.contains)) return false;
    return true;
}

/** The task type a smart prompt is about ("Type: coding" in its task section). */
function taskTypeOf(request: ChatCompletionRequest): string {
    const found = /^Type: (\w+)/m.exec(messageText(request));
    return found ? found[1] : 'general';
}

/**
 * The autopilot answers any request the way a model that simply gets on with
 * it would: refine, plan one task, do it, verify. With no scenario set, any
 * task runs to in-review.
 */
function autopilot(request: ChatCompletionRequest): ScenarioEntry {
    const call = (name: string, args: Record<string, unknown>): ScenarioEntry => ({ tool_call: { name, arguments: args } });
    switch (phaseOf(request)) {
        case 'refine':
            return call('finish_refinement', {
                spec: 'Do what the task says.',
                definition_of_done: ['The task is done as described.'],
                decisions: oneDecision('Take the task as written'),
            });
        case 'design':
            return call('finish_design', { design: 'Change the one file involved.', decisions: oneDecision('Keep the change minimal') });
        case 'test_plan':
            return call('finish_test_plan', { test_scenarios: ['The change is present and correct.'], decisions: oneDecision('One scenario is enough') });
        case 'plan': {
            const type = taskTypeOf(request);
            return call('create_tasks', {
                tasks: [{ key: 'work', title: 'Do the work', instructions: 'Complete the task as specified.', done_when: 'The work is finished.', type }],
                decisions: oneDecision('One task is enough'),
            });
        }
        case 'adjust':
            return call('finish_adjustment', { reason: 'Nothing more to change.', decisions: oneDecision('Proceed to verification') });
        case 'verify':
            return call('finish_verification', {
                passed: true,
                criteria: [{ criterion: 'The task is done as described.', passed: true, note: 'Confirmed by the executor report.' }],
                summary: 'E2E task completed and ready for review.',
                decisions: oneDecision('Accept the result'),
            });
        case 'checkpoint':
            return call('checkpoint', { progress: 'Working through the task.', next_step: 'Continue.' });
        case 'executor': {
            const finish = (request.tools || []).find((tool) => tool.function?.name === 'finish_work');
            const needsVerdict = (finish?.function?.parameters?.required || []).includes('verdict');
            return call('finish_work', {
                status: 'done',
                summary: 'E2E work completed.',
                evidence: ['Scripted by the E2E mock provider.'],
                ...(needsVerdict ? { verdict: 'approved' } : {}),
            });
        }
        default:
            return { text: 'E2E scripted reply.' };
    }
}

function chooseReply(state: MockState, request: ChatCompletionRequest): ScenarioEntry {
    for (const rule of state.rules) {
        if (!matches(rule.match, request)) continue;
        if (rule.used < rule.replies.length) return rule.replies[rule.used++];
        if (rule.repeat && rule.replies.length > 0) return rule.replies[rule.replies.length - 1];
    }
    return autopilot(request);
}

function completionBody(entry: ScenarioEntry, model: string, id: number): object {
    const calls = entry.tool_calls ?? (entry.tool_call ? [entry.tool_call] : []);
    const message: Record<string, unknown> = { role: 'assistant', content: entry.text ?? '' };
    if (calls.length > 0) {
        message.tool_calls = calls.map((toolCall, index) => ({
            id: `call_e2e_${id}_${index}`,
            type: 'function',
            function: { name: toolCall.name, arguments: JSON.stringify(toolCall.arguments) },
        }));
    }
    return {
        id: `chatcmpl-e2e-${id}`,
        object: 'chat.completion',
        created: Math.floor(Date.now() / 1000),
        model,
        choices: [{ index: 0, message, finish_reason: calls.length > 0 ? 'tool_calls' : 'stop' }],
        usage: USAGE_PER_CALL,
    };
}

/** The same completion as server-sent events, for callers that ask to stream. */
function writeStream(res: http.ServerResponse, body: any): void {
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' });
    const message = body.choices[0].message;
    const chunk = (delta: object, finish: string | null, usage?: object) => ({
        id: body.id, object: 'chat.completion.chunk', created: body.created, model: body.model,
        choices: [{ index: 0, delta, finish_reason: finish }], ...(usage ? { usage } : {}),
    });
    const send = (payload: object) => res.write(`data: ${JSON.stringify(payload)}\n\n`);
    send(chunk({ role: 'assistant', content: message.content || '' }, null));
    for (const [index, toolCall] of (message.tool_calls || []).entries()) {
        send(chunk({ tool_calls: [{ index, id: toolCall.id, function: toolCall.function }] }, null));
    }
    send(chunk({}, body.choices[0].finish_reason, body.usage));
    res.write('data: [DONE]\n\n');
    res.end();
}

function json(res: http.ServerResponse, status: number, payload: unknown): void {
    res.writeHead(status, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(payload));
}

async function parseRequestBody(req: http.IncomingMessage): Promise<unknown> {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk as Buffer);
    const raw = Buffer.concat(chunks).toString('utf8');
    if (!raw) return null;
    try {
        return JSON.parse(raw);
    } catch {
        return raw;
    }
}

function releaseHeld(state: MockState): number {
    const waiters = state.holdWaiters.splice(0);
    for (const resolve of waiters) resolve();
    return waiters.length;
}

function handleTestRoutes(req: http.IncomingMessage, res: http.ServerResponse, body: unknown, state: MockState): boolean {
    const url = req.url || '';
    if (url.startsWith('/__test/requests') && req.method === 'GET') {
        const completions = state.received.filter((entry) => entry.path.includes('/chat/completions'));
        json(res, 200, {
            count: state.received.length,
            completionsReceived: state.completionsReceived,
            completionsAnswered: state.completionsAnswered,
            held: state.held,
            requests: state.received,
            completions,
        });
        return true;
    }
    // Hold: completions that match (all of them when no match is given) block
    // after being logged, until released. A test uses it to catch the engine
    // provably in the middle of a model call.
    if (url === '/__test/hold' && req.method === 'POST') {
        state.holdAll = ((body as { match?: ScenarioMatch } | null)?.match) ?? {};
        json(res, 200, { status: 'ok', hold: true });
        return true;
    }
    if (url === '/__test/release' && req.method === 'POST') {
        state.holdAll = null;
        json(res, 200, { status: 'ok', released: releaseHeld(state) });
        return true;
    }
    if (url === '/__test/reset' && req.method === 'POST') {
        state.received.length = 0;
        state.completionsReceived = 0;
        state.completionsAnswered = 0;
        state.rules = [];
        state.holdAll = null;
        releaseHeld(state);
        json(res, 200, { status: 'ok' });
        return true;
    }
    if (url === '/__test/set-scenario' && req.method === 'POST') {
        const data = body as { rules?: ScenarioRule[]; append?: boolean } | null;
        const rules = (data?.rules || []).map((rule) => ({ ...rule, used: 0 }));
        state.rules = data?.append ? [...state.rules, ...rules] : rules;
        json(res, 200, { status: 'ok', rules: state.rules.length });
        return true;
    }
    if (url === '/__test/shutdown' && req.method === 'POST') {
        json(res, 200, { status: 'stopping' });
        setImmediate(() => { void state.shutdown?.(); });
        return true;
    }
    if (url === '/__test/health' && req.method === 'GET') {
        json(res, 200, { status: 'ok' });
        return true;
    }
    return false;
}

/**
 * A small HTTP server that stands in for an OpenAI-compatible LLM provider.
 *
 *   POST /v1/chat/completions  the scripted reply for the request, or the
 *                              autopilot's (see ScenarioRule and autopilot)
 *   GET  /v1/models            the models tests can configure
 *   POST /__test/set-scenario  {rules, append?}: script replies
 *   POST /__test/hold          {match?}: block matching completions
 *   POST /__test/release       unblock them
 *   GET  /__test/requests      everything received, completions with their phase and reply
 *   POST /__test/reset         forget everything
 */
export async function startMockProviderServer(): Promise<{ baseUrl: string; port: number; stop: () => Promise<void> }> {
    const state: MockState = {
        received: [],
        completionsReceived: 0,
        completionsAnswered: 0,
        rules: [],
        holdAll: null,
        holdWaiters: [],
        held: 0,
        shutdown: null,
    };
    const sockets = new Set<net.Socket>();

    const server = http.createServer(async (req, res) => {
        const body = await parseRequestBody(req);
        const url = req.url || '';
        if (url.startsWith('/__test/')) {
            if (!handleTestRoutes(req, res, body, state)) json(res, 404, { error: 'not_found', path: url });
            return;
        }
        const record: ReceivedRequest = { method: req.method || '', path: url, body, timestamp: Date.now() };
        state.received.push(record);

        if (url === '/v1/models' && req.method === 'GET') {
            json(res, 200, { object: 'list', data: MOCK_MODELS.map((id) => ({ id, object: 'model', owned_by: 'e2e' })) });
            return;
        }
        if (!url.includes('/chat/completions') || req.method !== 'POST') {
            json(res, 404, { error: 'not_found', method: req.method, path: url });
            return;
        }

        const request = (body || {}) as ChatCompletionRequest;
        const id = ++state.completionsReceived;
        record.phase = phaseOf(request);
        const reply = chooseReply(state, request);
        record.reply = reply;

        if (reply.hold || (state.holdAll && matches(state.holdAll, request))) {
            state.held++;
            await new Promise<void>((resolve) => state.holdWaiters.push(resolve));
            state.held--;
        }
        if (res.destroyed || !res.socket || res.socket.destroyed) return;

        state.completionsAnswered++;
        if (reply.status && reply.status >= 400) {
            json(res, reply.status, { error: { message: reply.text || 'scripted provider error', type: 'e2e_error' } });
            return;
        }
        const completion = completionBody(reply, request.model || MOCK_MODELS[0], id);
        if (request.stream) writeStream(res, completion);
        else json(res, 200, completion);
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
        state.holdAll = null;
        releaseHeld(state);
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
