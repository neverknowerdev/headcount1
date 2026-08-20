/** Resolve the backend-provided human-facing identity for a run. */
export function getRunAgentName(run: any): string | undefined {
    const explicit = run?.agent_name || run?.agent?.name || run?.task?.agent?.name;
    if (typeof explicit === 'string' && explicit.trim()) return explicit.trim();
    return undefined;
}
