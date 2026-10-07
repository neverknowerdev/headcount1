export interface TaskExecutionRun {
  task_id: number;
  kind?: string;
  started_at?: string;
  status?: string;
  agent_name?: string;
  agent?: { id?: number; name?: string };
}

/** Pick the most recent real agent session for each task. The configured
 * task.agent_id belongs in the Assignee column and is intentionally ignored. */
export function latestExecutionAgents(runs: TaskExecutionRun[]): Map<number, string> {
  const ordered = [...runs].sort((left, right) => {
    const leftActive = ['running', 'waiting', 'resuming', 'paused'].includes(left.status ?? '');
    const rightActive = ['running', 'waiting', 'resuming', 'paused'].includes(right.status ?? '');
    if (leftActive !== rightActive) return Number(rightActive) - Number(leftActive);
    const parsedLeft = left.started_at ? Date.parse(left.started_at) : Number.NEGATIVE_INFINITY;
    const parsedRight = right.started_at ? Date.parse(right.started_at) : Number.NEGATIVE_INFINITY;
    const leftDate = Number.isFinite(parsedLeft) ? parsedLeft : Number.NEGATIVE_INFINITY;
    const rightDate = Number.isFinite(parsedRight) ? parsedRight : Number.NEGATIVE_INFINITY;
    return rightDate - leftDate;
  });
  const result = new Map<number, string>();
  for (const run of ordered) {
    if ((run.kind !== 'agent_session' && run.kind !== 'ceo_consultation') || result.has(run.task_id)) continue;
    const name = run.agent?.name?.trim() || run.agent_name?.trim();
    if (name && name !== 'Orchestrator') result.set(run.task_id, name);
  }
  return result;
}
