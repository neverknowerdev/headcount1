import { APIRequestContext, Page, expect } from '@playwright/test';
import WebSocket from 'ws';

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
    while (Date.now() < deadline) {
        const res = await request.get(`/api/tasks/${taskId}`);
        if (res.ok()) {
            const task = await res.json();
            last = task;
            if (task.status === status) return;
        }
        await sleep(250);
    }

    let runLogHint = '';
    try {
        const companyId = last?.company_id ?? last?.CompanyID;
        if (companyId != null) {
            const runsRes = await request.get(`/api/runs?company_id=${companyId}`);
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
                    const log = latest.log_content || latest.LogContent || '';
                    const status = latest.status || latest.Status || '';
                    const sess = latest.session_id || latest.SessionID || '';
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
        `Last seen: ${JSON.stringify(last)}${runLogHint}`,
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
        const deadline = Date.now() + timeoutMs;
        const ws = new WebSocket(url);
        let timer: NodeJS.Timeout;

        const cleanup = () => {
            clearTimeout(timer);
            try { ws.close(); } catch { /* ignore */ }
        };

        timer = setInterval(() => {
            if (Date.now() > deadline) {
                cleanup();
                reject(new Error(`waitForHubEvent: timed out after ${timeoutMs}ms`));
            }
        }, 500);

        ws.on('open', () => { /* ready */ });
        ws.on('error', (err) => {
            cleanup();
            reject(new Error(`waitForHubEvent: ws error: ${err.message}`));
        });
        ws.on('message', (raw) => {
            try {
                const msg = JSON.parse(raw.toString());
                // Hub events are shaped { type, payload } (matches frontend store)
                const evt: HubEvent = { type: msg.type, data: msg.payload };
                if (predicate(evt)) {
                    cleanup();
                    resolve(evt);
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
    try {
        await waitForHubEvent(wsUrl, (e) => {
            if (e.type !== 'task_updated') return false;
            const d = e.data || {};
            return (d.id === taskId || d.ID === taskId) && d.status === status;
        }, timeoutMs);
        return;
    } catch (err) {
        // Fall back to polling
        console.log(`[helpers] WS wait failed (${(err as Error).message}); falling back to REST polling`);
        await waitForTaskStatus(request, taskId, status, timeoutMs);
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
    return new Promise<HubEvent>(async (resolve, reject) => {
        let resolved = false;
        const settle = (fn: () => void) => {
            if (resolved) return;
            resolved = true;
            fn();
        };
        const deadline = setTimeout(() => {
            if (resolved) return;
            // fall back to REST polling
            pollForComment(baseUrl, taskId, timeoutMs)
                .then((evt) => settle(() => resolve(evt)))
                .catch((err) => settle(() => reject(err)));
        }, 1000);
        try {
            const evt = await waitForHubEvent(wsUrl, (e) => {
                if (e.type !== 'comment_created') return false;
                const d = e.data || {};
                return (d.task_id === taskId || d.TaskID === taskId);
            }, Math.max(timeoutMs - 1000, 1000));
            clearTimeout(deadline);
            settle(() => resolve(evt));
        } catch (err) {
            clearTimeout(deadline);
            if (resolved) return;
            try {
                const evt = await pollForComment(baseUrl, taskId, timeoutMs);
                settle(() => resolve(evt));
            } catch (e2) {
                settle(() => reject(e2));
            }
        }
    });
}

async function pollForComment(
    baseUrl: string,
    taskId: number,
    timeoutMs: number,
): Promise<HubEvent> {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        const res = await fetch(`${baseUrl}/api/comments?task_id=${taskId}`);
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
