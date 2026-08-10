import type { AgentTokenStats, RunTokenStats } from '../components/RunLogViewer';

// buildAgentStats aggregates token stats per agent across a run's session
// tree: the root run plus each delegated child/descendant session, keyed by
// agent config name (multiple sessions of the same agent are summed).
export function buildAgentStats(root: any, sessions: any[]): AgentTokenStats[] {
    const order: string[] = [];
    const byAgent = new Map<string, RunTokenStats>();
    const numericKeys: (keyof RunTokenStats)[] = [
        'prompt_tokens', 'completion_tokens', 'reasoning_tokens',
        'tool_input_tokens', 'tool_output_tokens', 'cached_tokens',
        'total_tokens', 'mcp_tool_tokens',
    ];
    const add = (label: string, s: any) => {
        if (!s) return;
        if (!byAgent.has(label)) {
            byAgent.set(label, {});
            order.push(label);
        }
        const agg = byAgent.get(label)!;
        for (const k of numericKeys) {
            (agg as any)[k] = ((agg as any)[k] || 0) + (s[k] || 0);
        }
    };
    add(root.agent?.name || 'agent', root.token_stats);
    for (const s of sessions) {
        add(s.agent?.name || `run #${s.id}`, s.token_stats);
    }
    return order.map(agent => ({ agent, stats: byAgent.get(agent)! }));
}
