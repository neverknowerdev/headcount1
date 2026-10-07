import { describe, expect, it } from 'vitest';
import { latestExecutionAgents } from './taskExecutionAgents';

describe('latestExecutionAgents', () => {
  it('uses the newest execution run and ignores orchestrator runs and task assignees', () => {
    const names = latestExecutionAgents([
      { task_id: 1, kind: 'agent_session', started_at: '2026-01-01T10:00:00Z', agent: { name: 'Earlier Agent' } },
      { task_id: 1, kind: 'agent_session', status: 'running', started_at: '2026-01-01T09:00:00Z', agent: { name: 'Active Agent' } },
      { task_id: 1, kind: 'task_orchestrator', started_at: '2026-01-01T12:00:00Z', agent: { name: 'CEO' } },
      { task_id: 1, kind: 'agent_session', started_at: '2026-01-01T11:00:00Z', agent: { name: 'Worker Agent' } },
      { task_id: 2, kind: 'ceo_consultation', started_at: '2026-01-01T09:00:00Z', agent: { name: 'Review Agent' } },
      { task_id: 3, kind: 'helper_worker', started_at: '2026-01-01T13:00:00Z', agent: { name: 'Hidden Helper Identity' } },
    ]);

    expect(names.get(1)).toBe('Active Agent');
    expect(names.get(2)).toBe('Review Agent');
    expect(names.has(3)).toBe(false);
  });

  it('falls back to the run response name if the nested agent is absent', () => {
    expect(latestExecutionAgents([
      { task_id: 4, kind: 'agent_session', agent_name: 'Researcher' },
    ]).get(4)).toBe('Researcher');
  });
});
