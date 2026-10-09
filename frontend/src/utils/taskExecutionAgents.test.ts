import { describe, expect, it } from 'vitest';
import { latestExecutionAgents } from './taskExecutionAgents';

describe('latestExecutionAgents', () => {
  it('uses the newest session of a task, a live one first, and ignores task assignees', () => {
    const names = latestExecutionAgents([
      { task_id: 1, started_at: '2026-01-01T10:00:00Z', agent: { name: 'Earlier Agent' } },
      { task_id: 1, status: 'running', started_at: '2026-01-01T09:00:00Z', agent: { name: 'Active Agent' } },
      { task_id: 1, status: 'completed', started_at: '2026-01-01T11:00:00Z', agent: { name: 'Later Agent' } },
      { task_id: 2, status: 'completed', started_at: '2026-01-01T09:00:00Z', agent: { name: 'First Reviewer' } },
      { task_id: 2, status: 'completed', started_at: '2026-01-01T12:00:00Z', agent: { name: 'Second Reviewer' } },
    ]);

    expect(names.get(1)).toBe('Active Agent');
    expect(names.get(2)).toBe('Second Reviewer');
    expect(names.has(3)).toBe(false);
  });

  it('falls back to the run response name if the nested agent is absent', () => {
    expect(latestExecutionAgents([
      { task_id: 4, agent_name: 'Researcher' },
    ]).get(4)).toBe('Researcher');
  });
});
