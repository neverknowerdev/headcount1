import { describe, expect, it } from 'vitest';
import { buildAgentStats } from './runStats';

describe('buildAgentStats', () => {
    it('uses the database role key instead of editable agent names', () => {
        const stats = buildAgentStats(
            {
                agent: { name: 'Orchestrator', role_key: 'CEO', short_name: 'CEO' },
                token_stats: { total_tokens: 10 },
            },
            [
                {
                    agent: { name: 'Technical Lead', role_key: 'CTO' },
                    token_stats: { total_tokens: 20 },
                },
            ],
        );

        expect(stats.map(({ agent }) => agent)).toEqual(['CEO', 'CTO']);
    });

    it('falls back to the short name and then display name', () => {
        const stats = buildAgentStats(
            { agent: { name: 'Coder', short_name: 'CODER' }, token_stats: { total_tokens: 1 } },
            [{ agent: { name: 'QA' }, token_stats: { total_tokens: 2 } }],
        );

        expect(stats.map(({ agent }) => agent)).toEqual(['CODER', 'QA']);
    });
});
