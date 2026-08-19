import { describe, expect, it } from 'vitest';
import { getRunAgentName } from './runDisplay';

describe('getRunAgentName', () => {
    it('prefers the preloaded agent name', () => {
        expect(getRunAgentName({ name: 'GL-15-Coder-1-1', agent: { name: 'Coder Agent' } })).toBe('Coder Agent');
    });

    it('falls back to legacy agent_name and task agent metadata', () => {
        expect(getRunAgentName({ agent_name: 'CTO' })).toBe('CTO');
        expect(getRunAgentName({ task: { agent: { name: 'CEO Agent' } } })).toBe('CEO Agent');
    });

    it('derives the role from historical run names', () => {
        expect(getRunAgentName({ name: 'GL-12-Coder-1-1' })).toBe('Coder');
        expect(getRunAgentName({ name: 'GL-12-CTO-1-1' })).toBe('CTO');
        expect(getRunAgentName({ name: 'GL-12-orchestrator', kind: 'task_orchestrator' })).toBe('Orchestrator');
    });

    it('returns undefined when no identity is available', () => {
        expect(getRunAgentName({ name: 'legacy-run' })).toBeUndefined();
    });
});
