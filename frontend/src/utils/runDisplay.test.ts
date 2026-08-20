import { describe, expect, it } from 'vitest';
import { getRunAgentName } from './runDisplay';

describe('getRunAgentName', () => {
    it('prefers the preloaded agent name', () => {
        expect(getRunAgentName({ name: 'GL-15-Coder-1-1', agent: { name: 'Coder Agent' } })).toBe('Coder Agent');
        expect(getRunAgentName({ agent: { name: 'CEO Agent' }, agent_name: 'Orchestrator' })).toBe('Orchestrator');
    });

    it('falls back to legacy agent_name and task agent metadata', () => {
        expect(getRunAgentName({ agent_name: 'CTO' })).toBe('CTO');
        expect(getRunAgentName({ task: { agent: { name: 'CEO Agent' } } })).toBe('CEO Agent');
    });

    it('does not guess an identity from a run name', () => {
        expect(getRunAgentName({ name: 'GL-12-Coder-1-1', kind: 'agent_session' })).toBeUndefined();
        expect(getRunAgentName({ name: 'GL-15-orchestrator', kind: 'task_orchestrator' })).toBeUndefined();
    });
});
