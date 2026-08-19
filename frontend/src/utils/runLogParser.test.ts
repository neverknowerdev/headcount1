import { describe, expect, it } from 'vitest';
import { normalizeRunLogEntries, parseLogContent } from './runLogParser';

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

describe('normalizeRunLogEntries', () => {
  it('turns durable conversation messages into display rows', () => {
    const rows = normalizeRunLogEntries([
      { type: 'message', ts: '2026-08-19T00:00:00Z', seq: 1, content: JSON.stringify({ role: 'assistant', content: 'delegate', tool_calls: [{ id: 'c1' }] }) },
      { type: 'message', ts: '2026-08-19T00:00:01Z', seq: 2, content: JSON.stringify({ role: 'tool', name: 'run_new_session', tool_call_id: 'c1', content: 'session 38 queued' }) },
      { type: 'message', ts: '2026-08-19T00:00:02Z', seq: 3, content: JSON.stringify({ role: 'user', content: 'continue' }) },
    ]);

    expect(rows.map(row => row.entry.type)).toEqual(['response', 'tool_response', 'request']);
    expect(rows[0].entry.content).toContain('delegate');
    expect(rows[1].entry.tool_name).toBe('run_new_session');
    expect(rows[2].entry.content).toContain('continue');
    expect(rows.every(row => row.entry.content.startsWith('{') ? row.entry.type !== 'info' : true)).toBe(true);
  });

    it('also accepts viewer message wrappers', () => {
    const rows = normalizeRunLogEntries([
      { id: 4, entry: { type: 'message', seq: 4, content: JSON.stringify({ role: 'assistant', content: 'hello' }) } },
    ]);

    expect(rows).toHaveLength(1);
    expect(rows[0].entry.type).toBe('response');
        expect(rows[0].entry.content).toContain('hello');
    });

    it('normalizes legacy system messages for structured rendering', () => {
        const rows = normalizeRunLogEntries([
            { type: 'info', content: JSON.stringify({ role: 'system', content: 'legacy prompt' }) },
        ]);

        expect(rows).toHaveLength(1);
        expect(rows[0].entry.type).toBe('system');
        expect(rows[0].entry.content).toContain('legacy prompt');
    });
});
