import { describe, expect, it } from 'vitest';
import { getActivityAuthorLabel } from './activityDisplay';

describe('getActivityAuthorLabel', () => {
  it('uses the run agent name for agent comments', () => {
    expect(getActivityAuthorLabel({ author_type: 'agent', run_id: 42 }, [
      { id: 42, agent: { name: 'CTO' } },
    ])).toBe('🤖 CTO');
  });

  it('keeps human and unknown agent comments readable', () => {
    expect(getActivityAuthorLabel({ author_type: 'human' }, [])).toBe('👤 You');
    expect(getActivityAuthorLabel({ author_type: 'agent' }, [])).toBe('🤖 Agent');
  });
});
