export interface ParsedRunLogMessage {
    id: number;
    entry: Record<string, any>;
}

function stringifyToolContent(content: unknown): string {
    if (typeof content === 'string') return content;
    if (content == null) return '';
    return JSON.stringify(content);
}

// Normalize both structured run-log entries and legacy conversation JSONL.
// Standalone assistant/tool messages must become response/tool_response rows
// so RunLogViewer can pair them and render readable action cards.
export function parseLogContent(logContent: string): ParsedRunLogMessage[] {
    if (!logContent) return [];
    const lines = logContent.split('\n').filter((l: string) => l.trim());
    const messages: ParsedRunLogMessage[] = [];
    let i = 0;
    for (const line of lines) {
        const trimmed = line.trim();
        if (trimmed.startsWith('{')) {
            try {
                const parsed = JSON.parse(trimmed);
                if (parsed.type === 'tool_response' || parsed.type === 'tool_result' || parsed.type === 'tool') {
                    messages.push({ id: i++, entry: { ...parsed, type: 'tool_response', content: parsed.content || trimmed, tool_name: parsed.tool_name || parsed.name } });
                    continue;
                }
                if (typeof parsed.type === 'string') {
                    messages.push({ id: i++, entry: parsed });
                    continue;
                }
                if (parsed.role === 'assistant') {
                    messages.push({ id: i++, entry: { type: 'response', content: trimmed, model: parsed.model?.modelID || parsed.model } });
                    continue;
                }
                if (parsed.role === 'tool') {
                    messages.push({ id: i++, entry: {
                        type: 'tool_response', content: stringifyToolContent(parsed.content),
                        tool_name: parsed.tool_name || parsed.name || 'tool', tool_call_id: parsed.tool_call_id,
                    } });
                    continue;
                }
                if (parsed.role === 'user') {
                    messages.push({ id: i++, entry: { type: 'request', content: JSON.stringify({ messages: [parsed] }) } });
                    continue;
                }
                // Legacy text-log heuristics.
                if (parsed.agent && parsed.parts && Array.isArray(parsed.parts)) {
                    messages.push({ id: i++, entry: { type: 'request', content: trimmed, model: parsed.model?.modelID || parsed.model } });
                    continue;
                }
                if (parsed.info && parsed.parts && Array.isArray(parsed.parts)) {
                    messages.push({ id: i++, entry: { type: 'response', content: trimmed, status_code: 200 } });
                    continue;
                }
                if (parsed.messages && Array.isArray(parsed.messages)) {
                    messages.push({ id: i++, entry: { type: 'request', content: trimmed, model: parsed.model } });
                    continue;
                }
                if (parsed.choices && Array.isArray(parsed.choices)) {
                    messages.push({ id: i++, entry: { type: 'response', content: trimmed, status_code: 200 } });
                    continue;
                }
                if (parsed.reasoning || parsed.tokens || parsed.raw) {
                    messages.push({ id: i++, entry: { type: 'response', content: trimmed, status_code: 200 } });
                    continue;
                }
            } catch { /* treat as info */ }
        }
        messages.push({ id: i++, entry: { type: 'info', content: trimmed } });
    }
    return messages;
}
