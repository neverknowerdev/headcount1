import { APIRequestContext } from '@playwright/test';
import WebSocket from 'ws';
import { fetchWithTimeout } from './http';

export interface HubEvent {
    type: string;
    data: any;
}

/**
 * Wait for a task to reach a given status by polling the REST API.
 * Falls back to this if WebSocket events are flaky or unavailable.
 *
 * On timeout, also fetches the latest run log for the task's company and
 * includes it in the thrown error so test failures show what the engine
 * actually did (or didn't do).
 */
export async function waitForTaskStatus(
    request: APIRequestContext,
    taskId: number,
    status: string,
    timeoutMs = 30_000,
): Promise<void> {
    const deadline = Date.now() + timeoutMs;
    let last: any = null;
    let lastRequestError = '';
    while (Date.now() < deadline) {
        try {
            const remaining = Math.max(1, deadline - Date.now());
            const res = await request.get(`/api/tasks/${taskId}`, { timeout: Math.min(5_000, remaining) });
            if (res.ok()) {
                const task = await res.json();
                last = task;
                if (task.status === status) return;
                if (['failed', 'canceled', 'stale', 'recoverable_failed'].includes(task.status) && task.status !== status) {
                    throw new Error(`task entered terminal status "${task.status}" before expected "${status}"`);
                }
            }
        } catch (err) {
            if (err instanceof Error && err.message.startsWith('task entered terminal status')) throw err;
            // A server restart, connection pool hiccup, or transient socket
            // reset must not fail the whole Playwright test immediately. Keep
            // polling until the deadline, then include the last error in the
            // diagnostic below so retries remain useful.
            lastRequestError = (err as Error).message;
        }
        await sleep(250);
    }

    let runLogHint = '';
    try {
        const companyId = last?.company_id ?? last?.CompanyID;
        if (companyId != null) {
            const runsRes = await request.get(`/api/runs?company_id=${companyId}`, { timeout: 3_000 });
            if (runsRes.ok()) {
                const runs = await runsRes.json();
                const mine = (runs as any[])
                    .filter((r) => r.task_id === taskId || r.TaskID === taskId)
                    .sort((a, b) => {
                        const ta = new Date(a.started_at || a.StartedAt || 0).getTime();
                        const tb = new Date(b.started_at || b.StartedAt || 0).getTime();
                        return tb - ta;
                    });
                if (mine.length > 0) {
                    const latest = mine[0];
                    // The list endpoint omits log_content/log_entries (they're
                    // full transcripts, only fetched lazily by the Run Log
                    // Details page) — re-fetch the single run for diagnostics.
                    let full: any = latest;
                    try {
                        const runRes = await request.get(`/api/runs/${latest.id}`, { timeout: 3_000 });
                        if (runRes.ok()) full = await runRes.json();
                    } catch { /* fall back to list data below */ }
                    const log = full.log_content || full.LogContent || '';
                    const status = full.status || full.Status || '';
                    const sess = full.session_id || full.SessionID || '';
                    runLogHint =
                        `\nLatest run for task ${taskId}: status="${status}" session="${sess}"\n` +
                        `Run log (last 2000 chars):\n${log.slice(-2000)}`;
                } else {
                    runLogHint = `\nNo runs found for task ${taskId}. runs endpoint returned ${runs.length} total runs; first run keys: ${runs.length > 0 ? Object.keys(runs[0]).join(',') : '(empty)'}`;
                }
            } else {
                runLogHint = `\nRuns endpoint returned status ${runsRes.status()}`;
            }
        } else {
            runLogHint = `\nNo company_id on task; cannot fetch runs.`;
        }
    } catch (e) {
        runLogHint = `\nFailed to fetch runs: ${(e as Error).message}`;
    }

    throw new Error(
        `waitForTaskStatus: task ${taskId} did not reach status "${status}" within ${timeoutMs}ms. ` +
        `Last seen: ${JSON.stringify(last)}${lastRequestError ? `\nLast request error: ${lastRequestError}` : ''}${runLogHint}`,
    );
}

/**
 * Wait for any event from the WebSocket hub matching the given predicate.
 * Used in tests to wait for `comment_created`, `run_ended`, `task_updated`, etc.
 */
export async function waitForHubEvent(
    url: string,
    predicate: (e: HubEvent) => boolean,
    timeoutMs = 30_000,
): Promise<HubEvent> {
    return new Promise((resolve, reject) => {
        const ws = new WebSocket(url);
        let timer: NodeJS.Timeout;
        let settled = false;

        const cleanup = () => {
            clearTimeout(timer);
            try { ws.close(); } catch { /* ignore */ }
        };

        const fail = (err: Error) => {
            if (settled) return;
            settled = true;
            cleanup();
            reject(err);
        };
        const succeed = (evt: HubEvent) => {
            if (settled) return;
            settled = true;
            cleanup();
            resolve(evt);
        };

        timer = setTimeout(() => fail(new Error(`waitForHubEvent: timed out after ${timeoutMs}ms`)), timeoutMs);

        ws.on('open', () => { /* ready */ });
        ws.on('error', (err) => fail(new Error(`waitForHubEvent: ws error: ${err.message}`)));
        ws.on('close', (code, reason) => {
            if (!settled) fail(new Error(`waitForHubEvent: ws closed before matching event (${code}${reason ? `: ${reason}` : ''})`));
        });
        ws.on('message', (raw) => {
            try {
                const msg = JSON.parse(raw.toString());
                // Hub events are shaped { type, payload } (matches frontend store)
                const evt: HubEvent = { type: msg.type, data: msg.payload };
                if (predicate(evt)) {
                    succeed(evt);
                }
            } catch {
                /* ignore non-JSON */
            }
        });
    });
}

/**
 * Convenience: wait for a `task_updated` event whose `data.id` matches and
 * `data.status` matches.
 */
export async function waitForTaskStatusEvent(
    request: APIRequestContext,
    baseUrl: string,
    taskId: number,
    status: string,
    timeoutMs = 30_000,
): Promise<void> {
    const wsUrl = baseUrl.replace(/^http/, 'ws') + '/api/ws';
    const deadline = Date.now() + timeoutMs;
    try {
        await waitForHubEvent(wsUrl, (e) => {
            if (e.type !== 'task_updated') return false;
            const d = e.data || {};
            return (d.id === taskId || d.ID === taskId) && d.status === status;
        }, Math.min(1_000, Math.max(1, timeoutMs)));
        return;
    } catch (err) {
        const remaining = deadline - Date.now();
        if (remaining <= 0) throw err;
        // Fall back to polling within the original total deadline.
        console.log(`[helpers] WS wait failed (${(err as Error).message}); falling back to REST polling`);
        await waitForTaskStatus(request, taskId, status, remaining);
    }
}

/**
 * Convenience: wait for a `comment_created` event for a given task id.
 * Falls back to polling the REST API if the WebSocket misses the event
 * (the engine's events fire before the test can connect a WS in some flows).
 */
export async function waitForComment(
    baseUrl: string,
    taskId: number,
    timeoutMs = 30_000,
): Promise<HubEvent> {
    const wsUrl = baseUrl.replace(/^http/, 'ws') + '/api/ws';
    const deadline = Date.now() + timeoutMs;
    try {
        return await waitForHubEvent(wsUrl, (e) => {
            if (e.type !== 'comment_created') return false;
            const d = e.data || {};
            return (d.task_id === taskId || d.TaskID === taskId);
        }, Math.min(1_000, Math.max(1, timeoutMs)));
    } catch (err) {
        const remaining = deadline - Date.now();
        if (remaining <= 0) throw err;
        console.log(`[helpers] WS comment wait failed (${(err as Error).message}); falling back to REST polling`);
        return pollForComment(baseUrl, taskId, remaining);
    }
}

async function pollForComment(
    baseUrl: string,
    taskId: number,
    timeoutMs: number,
): Promise<HubEvent> {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        const res = await fetchWithTimeout(
            `${baseUrl}/api/comments?task_id=${taskId}`,
            {},
            Math.min(3_000, Math.max(1, deadline - Date.now())),
        );
        if (res.ok) {
            const comments = await res.json();
            if (Array.isArray(comments) && comments.length > 0) {
                return { type: 'comment_created', data: comments[0] };
            }
        }
        await sleep(500);
    }
    throw new Error(`pollForComment: no comment found for task ${taskId} within ${timeoutMs}ms`);
}

function sleep(ms: number) {
    return new Promise((r) => setTimeout(r, ms));
}
