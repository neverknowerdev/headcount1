export function getActivityAuthorLabel(comment: any, runs: any[]): string {
    if (comment?.author_type !== 'agent') return '👤 You';
    const run = comment?.run_id ? runs.find((candidate: any) => candidate.id === comment.run_id) : undefined;
    const agentName = run?.agent?.name || run?.agent_name;
    return agentName ? `🤖 ${agentName}` : '🤖 Agent';
}
