import { describe, expect, it } from 'vitest';
import { parseLogContent } from './runLogParser';

describe('parseLogContent', () => {
  it('normalizes assistant tool calls and tool results into paired entries', () => {
    const messages = parseLogContent([
      JSON.stringify({ role: 'assistant', content: '', tool_calls: [{ id: 'c1', type: 'function', function: { name: 'run_new_session', arguments: '{"agent_name":"CTO"}' } }] }),
      JSON.stringify({ role: 'tool', tool_call_id: 'c1', name: 'run_new_session', content: '{"id":42,"status":"queued"}' }),
    ].join('\n'));

    expect(messages.map(m => m.entry.type)).toEqual(['response', 'tool_response']);
    expect(messages[1].entry.tool_name).toBe('run_new_session');
  });

  it('keeps structured entries and legacy request lines intact', () => {
    const messages = parseLogContent([
      JSON.stringify({ type: 'tool_call', tool_name: 'ask_human', content: '{"question":"Approve?"}' }),
      JSON.stringify({ messages: [{ role: 'user', content: 'task description' }] }),
    ].join('\n'));

    expect(messages[0].entry.type).toBe('tool_call');
    expect(messages[1].entry.type).toBe('request');
  });
});
