export interface TaskExecutionRun {
  task_id: number;
  started_at?: string;
  status?: string;
  agent_name?: string;
  agent?: { id?: number; name?: string };
}

const ACTIVE_STATUSES = ['running', 'resuming', 'paused'];

/** Pick the agent of the most recent executor session of each task, a live
 * session first. The configured task.agent_id belongs in the Assignee column
 * and is intentionally ignored. */
export function latestExecutionAgents(runs: TaskExecutionRun[]): Map<number, string> {
  const ordered = [...runs].sort((left, right) => {
    const leftActive = ACTIVE_STATUSES.includes(left.status ?? '');
    const rightActive = ACTIVE_STATUSES.includes(right.status ?? '');
    if (leftActive !== rightActive) return Number(rightActive) - Number(leftActive);
    const parsedLeft = left.started_at ? Date.parse(left.started_at) : Number.NEGATIVE_INFINITY;
    const parsedRight = right.started_at ? Date.parse(right.started_at) : Number.NEGATIVE_INFINITY;
    const leftDate = Number.isFinite(parsedLeft) ? parsedLeft : Number.NEGATIVE_INFINITY;
    const rightDate = Number.isFinite(parsedRight) ? parsedRight : Number.NEGATIVE_INFINITY;
    return rightDate - leftDate;
  });
  const result = new Map<number, string>();
  for (const run of ordered) {
    if (result.has(run.task_id)) continue;
    const name = run.agent?.name?.trim() || run.agent_name?.trim();
    if (name) result.set(run.task_id, name);
  }
  return result;
}
