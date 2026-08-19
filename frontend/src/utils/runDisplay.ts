/** Resolve the human-facing agent name for a run, including legacy payloads. */
export function getRunAgentName(run: any): string | undefined {
    const explicit = run?.agent?.name || run?.agent_name || run?.task?.agent?.name;
    if (typeof explicit === 'string' && explicit.trim()) return explicit.trim();

    // Older task-run payloads did not preload Agent. The generated run name
    // still contains the selected role, so keep historical runs readable.
    const runName = typeof run?.name === 'string' ? run.name : '';
    const roleMatch = runName.match(/^[^-]+-\d+-(.+?)-\d+(?:-\d+)?$/);
    if (roleMatch?.[1]) return roleMatch[1];
    if (run?.kind === 'task_orchestrator' || /-orchestrator$/.test(runName)) return 'Orchestrator';
    return undefined;
}
