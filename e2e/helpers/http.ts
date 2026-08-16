/**
 * Bounded HTTP primitives for the harness. Node's fetch does not have a
 * default response timeout, so a server that accepts a socket and never
 * responds can otherwise outlive every polling deadline around it.
 */
export async function fetchWithTimeout(
    input: string | URL,
    init: RequestInit = {},
    timeoutMs = 10_000,
): Promise<Response> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const upstream = init.signal;
    const abortUpstream = () => controller.abort();
    upstream?.addEventListener('abort', abortUpstream, { once: true });
    try {
        return await fetch(input, { ...init, signal: controller.signal });
    } finally {
        clearTimeout(timer);
        upstream?.removeEventListener('abort', abortUpstream);
    }
}

export async function requireFetchOK(
    input: string | URL,
    init: RequestInit = {},
    timeoutMs = 10_000,
): Promise<Response> {
    const response = await fetchWithTimeout(input, init, timeoutMs);
    if (response.ok) return response;
    let body = '';
    try { body = await response.text(); } catch { /* diagnostic only */ }
    throw new Error(`${init.method || 'GET'} ${input} failed: ${response.status} ${body}`);
}

export async function fetchJSON<T = any>(
    input: string | URL,
    init: RequestInit = {},
    timeoutMs = 10_000,
): Promise<T> {
    const response = await requireFetchOK(input, init, timeoutMs);
    return response.json() as Promise<T>;
}
