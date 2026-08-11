import type { AgentTokenStats, RunTokenStats } from '../components/RunLogViewer';

// Agent role keys are the stable database-owned identity. Names are editable
// UI text, so using them here would split or rename a role in the breakdown
// when an agent is renamed.
function agentLabel(run: any): string {
    return run?.agent?.role_key || run?.agent?.short_name || run?.agent?.name ||
        (run?.id ? `run #${run.id}` : 'agent');
}

// buildAgentStats aggregates token stats per agent across a run's session
// tree: the root run plus each delegated child/descendant session, keyed by
// the database-owned role identity (multiple sessions of the same role are
// summed).
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
    add(agentLabel(root), root.token_stats);
    for (const s of sessions) {
        add(agentLabel(s), s.token_stats);
    }
    return order.map(agent => ({ agent, stats: byAgent.get(agent)! }));
}
