import React, { useState, useEffect, useRef, useMemo } from 'react';
import {
  ChevronDown, ChevronRight,
  Bot, User, AlertCircle, Loader2, Code2, FileText,
  FileText as FileIcon, Terminal, Search, ListChecks,
  Wrench, Brain, ChevronUp
} from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

// Token usage shape for a single LLM call. Mirrors the
// pkg/tokens/tokens.go Usage struct on the Go side.
interface TokenUsage {
  prompt?: number;
  completion?: number;
  total?: number;
  reasoning?: number;
  tool_input?: number;
}

// Per-run aggregates persisted in Run.token_stats on the backend.
interface RunTokenStats {
  prompt_tokens?: number;
  completion_tokens?: number;
  reasoning_tokens?: number;
  tool_input_tokens?: number;
  tool_output_tokens?: number;
  total_tokens?: number;
}

interface LogEntry {
  type: 'info' | 'request' | 'response' | 'tool_call' | 'tool_response' | 'error';
  content: string;
  model?: string;
  status_code?: number;
  tool_name?: string;
  agent_name?: string;
  ts?: string;
  prompt_tokens?: number;
  output_tokens?: number;
  input_tokens?: number;
}

interface LogMessage {
  id: number;
  entry: LogEntry;
}

interface RunLogViewerProps {
  messages: LogMessage[];
  status?: string;
  autoScroll?: boolean;
  tokenStats?: RunTokenStats | null;
}

function JsonBlock({ data }: { data: string }) {
  let formatted = data;
  try {
    formatted = JSON.stringify(JSON.parse(data), null, 2);
  } catch {}
  return (
    <pre className="p-2 text-xs font-mono text-gray-800 bg-gray-50 rounded overflow-x-auto whitespace-pre-wrap max-h-96 overflow-y-auto border border-gray-200">
      {formatted}
    </pre>
  );
}

// Extract the user-facing content from a request entry.
function getUserMessage(content: string): string | null {
  try {
    const parsed = JSON.parse(content);
    if (parsed.parts && Array.isArray(parsed.parts)) {
      const textParts = parsed.parts
        .filter((p: any) => p.type === 'text' && p.text)
        .map((p: any) => p.text);
      if (textParts.length > 0) return textParts.join('\n\n');
    }
    if (parsed.messages && Array.isArray(parsed.messages)) {
      const userMsgs = parsed.messages
        .filter((m: any) => m.role === 'user' && m.content)
        .map((m: any) => m.content);
      if (userMsgs.length > 0) return userMsgs.join('\n\n');
    }
    return null;
  } catch {
    return null;
  }
}

// Extract agent text, reasoning, and tool calls from a response entry.
function getAgentMessage(content: string): {
  text: string | null;
  reasoning: string | null;
  toolCalls: any[];
  tokens: TokenUsage | null;
} {
  try {
    const parsed = JSON.parse(content);
    // OpenCode response shape: { info, parts: [{type: "text", text}, {type: "tool_call", ...}] }
    if (parsed.parts && Array.isArray(parsed.parts)) {
      const textParts = parsed.parts
        .filter((p: any) => p.type === 'text' && p.text)
        .map((p: any) => p.text);
      const toolCalls = parsed.parts
        .filter((p: any) => p.type === 'tool_call' || p.tool_call)
        .map((p: any) => p.tool_call || p);
      if (textParts.length > 0 || toolCalls.length > 0) {
        return { text: textParts.join('\n\n') || null, reasoning: null, toolCalls, tokens: null };
      }
    }
    // LLM provider response shape: { choices: [{message: {content, tool_calls}}] }
    if (parsed.choices && Array.isArray(parsed.choices)) {
      const textParts = parsed.choices
        .map((c: any) => c.message?.content || c.delta?.content || c.text || '')
        .filter(Boolean);
      const toolCalls = parsed.choices
        .flatMap((c: any) => c.message?.tool_calls || c.delta?.tool_calls || []);
      if (textParts.length > 0 || toolCalls.length > 0) {
        return { text: textParts.join('\n\n') || null, reasoning: null, toolCalls, tokens: null };
      }
    }
    // Proxy streaming response shape: { content, reasoning, tool_calls, tokens, raw }
    // The raw field carries the original provider response body; if it parses
    // as one of the above shapes we still want the proxy-side metadata to
    // take priority (so reasoning text and token breakdown show up).
    if (parsed.reasoning || parsed.content || parsed.tool_calls || parsed.tokens) {
      const tokens: TokenUsage = parsed.tokens || null;
      // If the response came from the non-streaming path, the body is
      // under .raw; try to extract the assistant text from it.
      let text = parsed.content || null;
      if (!text && typeof parsed.raw === 'string') {
        try {
          const rawParsed = JSON.parse(parsed.raw);
          if (rawParsed.choices && Array.isArray(rawParsed.choices)) {
            text = rawParsed.choices
              .map((c: any) => c.message?.content || '')
              .filter(Boolean)
              .join('\n\n') || null;
          }
        } catch {}
      }
      return {
        text,
        reasoning: parsed.reasoning || null,
        toolCalls: Array.isArray(parsed.tool_calls) ? parsed.tool_calls : [],
        tokens,
      };
    }
    return { text: null, reasoning: null, toolCalls: [], tokens: null };
  } catch {
    return { text: null, reasoning: null, toolCalls: [], tokens: null };
  }
}

// Format token count: 8500 -> "8.5K", 150000 -> "150K", 1200000 -> "1.2M"
function formatTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'K';
  if (n < 1000000) return Math.round(n / 1000) + 'K';
  return (n / 1000000).toFixed(1).replace(/\.0$/, '') + 'M';
}

// Format a timestamp as HH:MM:SS
function formatTime(ts?: string): string {
  if (!ts) return '';
  try {
    const d = new Date(ts);
    return d.toLocaleTimeString('en-GB', { hour12: false });
  } catch {
    return '';
  }
}

// Per-tool icon: maps a tool name to a lucide component + accent color class.
function getToolIcon(toolName: string): { Icon: any; color: string; bg: string } {
  switch (toolName) {
    case 'read':
      return { Icon: FileIcon, color: 'text-blue-600', bg: 'bg-blue-100' };
    case 'write':
      return { Icon: FileText, color: 'text-green-600', bg: 'bg-green-100' };
    case 'edit':
      return { Icon: FileText, color: 'text-orange-600', bg: 'bg-orange-100' };
    case 'bash':
      return { Icon: Terminal, color: 'text-gray-700', bg: 'bg-gray-800 text-green-400' };
    case 'glob':
    case 'grep':
    case 'rg':
      return { Icon: Search, color: 'text-purple-600', bg: 'bg-purple-100' };
    case 'update_task_status':
      return { Icon: ListChecks, color: 'text-green-700', bg: 'bg-green-100' };
    case 'todowrite':
      return { Icon: ListChecks, color: 'text-blue-600', bg: 'bg-blue-100' };
    default:
      return { Icon: Wrench, color: 'text-amber-600', bg: 'bg-amber-100' };
  }
}

// Get a short description of what a tool call does (for the collapsed row).
function getToolCallPreview(entry: LogEntry): string {
  try {
    const args = JSON.parse(entry.content);
    if (args.filePath) return String(args.filePath);
    if (args.command) return String(args.command);
    if (args.pattern) return String(args.pattern);
    if (args.status) return `status: ${args.status}`;
  } catch {}
  return entry.tool_name || 'tool call';
}

// Render the icon for an entry type
function EntryIcon({ entry, size = 14 }: { entry: LogEntry; size?: number }) {
  if (entry.type === 'tool_call' || entry.type === 'tool_response') {
    const { Icon, color } = getToolIcon(entry.tool_name || '');
    return <Icon size={size} className={color} />;
  }
  switch (entry.type) {
    case 'request':
      return <User size={size} className="text-blue-600" />;
    case 'response':
      return <Bot size={size} className="text-indigo-600" />;
    case 'error':
      return <AlertCircle size={size} className="text-red-600" />;
    default:
      return null;
  }
}

// Get a short role label for the entry
function getRoleLabel(entry: LogEntry): string {
  switch (entry.type) {
    case 'request':
      return entry.agent_name || 'User';
    case 'response':
      return entry.agent_name || 'Agent';
    case 'tool_call':
      return entry.tool_name || 'tool';
    case 'tool_response':
      return `${entry.tool_name || 'tool'} ▾`;
    case 'error':
      return 'Error';
    default:
      return 'Info';
  }
}

// Get a short preview string from entry content for the collapsed view.
function getPreview(entry: LogEntry): string {
  if (entry.type === 'info') return entry.content;
  if (entry.type === 'error') return entry.content;
  if (entry.type === 'tool_call') return getToolCallPreview(entry);
  if (entry.type === 'tool_response') {
    // For tool_response, show the first non-empty line of the response
    const firstLine = entry.content.split('\n').find((l: string) => l.trim().length > 0) || '';
    return firstLine.length > 80 ? firstLine.slice(0, 80) + '…' : firstLine;
  }
  if (entry.type === 'request') {
    const msg = getUserMessage(entry.content);
    if (msg) return msg.split('\n').find((l: string) => l.trim().length > 0) || msg;
    return entry.content;
  }
  if (entry.type === 'response') {
    const { text, toolCalls } = getAgentMessage(entry.content);
    if (text) {
      const firstLine = text.split('\n').find((l: string) => l.trim().length > 0) || text;
      if (toolCalls.length > 0) {
        const names = toolCalls.map((tc: any) => tc.name || tc.function?.name).filter(Boolean).join(', ');
        return `${firstLine.slice(0, 50)}${firstLine.length > 50 ? '…' : ''} · ${names}`;
      }
      return firstLine;
    }
    if (toolCalls.length > 0) {
      const names = toolCalls.map((tc: any) => tc.name || tc.function?.name).filter(Boolean).join(', ');
      return names || 'tool call';
    }
    return entry.content;
  }
  return entry.content;
}

// Get token count to display on a row. For tool_call we prefer the
// input_tokens field (the args size) since that's what the LLM "spent".
// For tool_response we show the response size. For responses we sum
// prompt + completion + reasoning (NOT the provider's reported total,
// which excludes reasoning for o-series models). The request row shows
// the prompt size (which on subsequent turns is auto-injected by the
// proxy once the response comes back).
function getRowTokens(entry: LogEntry): number | null {
  if (entry.type === 'response') {
    const { tokens } = getAgentMessage(entry.content);
    if (tokens) {
      return (tokens.prompt || 0) + (tokens.completion || 0) + (tokens.reasoning || 0);
    }
  }
  if (entry.type === 'tool_call') {
    if (typeof entry.input_tokens === 'number') return entry.input_tokens;
    if (typeof entry.output_tokens === 'number') return entry.output_tokens;
  }
  if (entry.type === 'tool_response' && typeof entry.output_tokens === 'number') {
    return entry.output_tokens;
  }
  if (entry.type === 'request' && typeof entry.prompt_tokens === 'number') {
    return entry.prompt_tokens;
  }
  return null;
}

// Per-row token breakdown, used by the expanded-view footer. For a
// request row this includes the request itself plus any tool responses
// that follow it up to the next request/response. For a response row it
// returns the provider-reported prompt/completion/reasoning split and
// computes the visible total as the sum of those (NOT the provider's
// reported total, which is just prompt+completion and excludes reasoning
// for o-series models).
function getRowTokenBreakdown(entry: LogEntry, allEntries: LogMessage[], index: number): {
  prompt?: number;
  completion?: number;
  reasoning?: number;
  tool_input?: number;
  tool_output?: number;
  total?: number;
} | null {
  if (entry.type === 'response') {
    const { tokens } = getAgentMessage(entry.content);
    if (!tokens) return null;
    const sum = (tokens.prompt || 0) + (tokens.completion || 0) + (tokens.reasoning || 0);
    return {
      prompt: tokens.prompt,
      completion: tokens.completion,
      reasoning: tokens.reasoning,
      total: sum,
    };
  }
  if (entry.type === 'request') {
    // Walk forward and tally tool responses that follow this request
    // (the LLM will see all of them in its next prompt).
    const reqTokens = entry.prompt_tokens || 0;
    let toolIn = 0;
    let toolOut = 0;
    for (let i = index + 1; i < allEntries.length; i++) {
      const e = allEntries[i].entry;
      if (e.type === 'request' || e.type === 'response') break;
      if (e.type === 'tool_call' && typeof e.input_tokens === 'number') {
        toolIn += e.input_tokens;
      }
      if (e.type === 'tool_response' && typeof e.output_tokens === 'number') {
        toolOut += e.output_tokens;
      }
    }
    if (!reqTokens && !toolIn && !toolOut) return null;
    return { prompt: reqTokens, tool_input: toolIn, tool_output: toolOut, total: reqTokens + toolIn + toolOut };
  }
  return null;
}

// Get the color of a row based on entry type
function getRowColor(entry: LogEntry): string {
  switch (entry.type) {
    case 'request': return 'text-blue-700';
    case 'response': return 'text-indigo-700';
    case 'tool_call': return 'text-amber-700';
    case 'tool_response': return 'text-amber-500';
    case 'error': return 'text-red-700';
    default: return 'text-gray-500';
  }
}

interface CollapsibleRowProps {
  entry: LogEntry;
  rawMode: boolean;
  allEntries: LogMessage[];
  index: number;
}

// Single row in the list - can be expanded to show details.
function CollapsibleRow({ entry, rawMode, allEntries, index }: CollapsibleRowProps) {
  const [expanded, setExpanded] = useState(false);
  const time = formatTime(entry.ts);
  const preview = useMemo(() => getPreview(entry), [entry]);
  const previewTruncated = preview.length > 80 ? preview.slice(0, 80) + '…' : preview;
  const tokens = getRowTokens(entry);
  const isError = entry.type === 'error';

  // For response entries, also collect tool call entries that follow this
  // response but precede the next request - these are the tool calls that
  // were made as a result of this response.
  const relatedToolCalls = useMemo(() => {
    if (entry.type !== 'response') return [];
    const result: LogEntry[] = [];
    for (let i = index + 1; i < allEntries.length; i++) {
      const e = allEntries[i].entry;
      if (e.type === 'request' || e.type === 'response') break;
      if (e.type === 'tool_call') result.push(e);
    }
    return result;
  }, [entry, allEntries, index]);

  // For tool_call entries, find the following tool_response (if any) so
  // we can show input→output token sizes side by side on the tool block.
  // We look across the entire rest of the run for a matching tool_response
  // by name (the proxy-side tool result logging may emit it after the
  // next request/response, not directly after the tool_call).
  const pairedToolResponse = useMemo(() => {
    if (entry.type !== 'tool_call') return null;
    let best: LogEntry | null = null;
    for (let i = index + 1; i < allEntries.length; i++) {
      const e = allEntries[i].entry;
      if (e.type === 'tool_response' && e.tool_name === entry.tool_name) {
        if (!best) best = e;
        // Stop once we hit a tool_response for a different tool — that
        // means we've moved past the matched pair (don't keep walking
        // through the whole run).
        break;
      }
    }
    return best;
  }, [entry, allEntries, index]);

  return (
    <div className={`border-b border-gray-100 ${isError ? 'bg-red-50' : ''}`}>
      {/* Collapsed row */}
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-gray-50 text-left text-sm"
      >
        <span className="shrink-0 text-gray-400">
          {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </span>
        <span className="shrink-0 w-5 h-5 flex items-center justify-center">
          {entry.type === 'info' ? <span className="text-gray-400 text-xs">•</span> : <EntryIcon entry={entry} />}
        </span>
        <span className={`shrink-0 font-medium text-xs uppercase tracking-wide ${getRowColor(entry)}`}>
          {getRoleLabel(entry)}
        </span>
        <span className="flex-1 truncate text-gray-600 text-xs" title={preview}>
          {previewTruncated}
        </span>
        {relatedToolCalls.length > 0 && (
          <span className="shrink-0 text-xs text-amber-600 bg-amber-50 px-1.5 py-0.5 rounded">
            {relatedToolCalls.length} tool
          </span>
        )}
        {pairedToolResponse && (pairedToolResponse.output_tokens || 0) > 0 && (
          <span className="shrink-0 text-xs text-amber-600 bg-amber-50 px-1.5 py-0.5 rounded font-mono">
            ← {formatTokens(pairedToolResponse.output_tokens || 0)} tok
          </span>
        )}
        {time && <span className="shrink-0 text-xs text-gray-400 font-mono">{time}</span>}
        {tokens !== null && tokens > 0 && (
          <span className="shrink-0 text-xs text-gray-500 font-mono bg-gray-100 px-1.5 py-0.5 rounded">
            {formatTokens(tokens)} tok
          </span>
        )}
      </button>

      {/* Expanded content */}
      {expanded && (
        <div className="px-3 pb-3 pt-1 bg-gray-50/50">
          <ExpandedContent
            entry={entry}
            rawMode={rawMode}
            relatedToolCalls={relatedToolCalls}
            allEntries={allEntries}
            index={index}
          />
        </div>
      )}
    </div>
  );
}

// Expanded view for any entry type.
function ExpandedContent({ entry, rawMode, relatedToolCalls, allEntries, index }: {
  entry: LogEntry;
  rawMode: boolean;
  relatedToolCalls: LogEntry[];
  allEntries: LogMessage[];
  index: number;
}) {
  if (entry.type === 'info') {
    return <div className="text-xs text-gray-600 whitespace-pre-wrap break-words pl-7">{entry.content}</div>;
  }
  if (entry.type === 'error') {
    return <div className="text-xs text-red-700 whitespace-pre-wrap break-words pl-7 font-medium">{entry.content}</div>;
  }
  if (entry.type === 'tool_call') {
    return <ToolCallExpanded entry={entry} />;
  }
  if (entry.type === 'tool_response') {
    return <ToolResponseExpanded entry={entry} />;
  }
  if (entry.type === 'request') {
    const msg = getUserMessage(entry.content);
    const breakdown = getRowTokenBreakdown(entry, allEntries, index);
    return (
      <div className="pl-7 space-y-2">
        {msg && !rawMode && (
          <div className="bg-white border border-blue-100 rounded-lg px-3 py-2">
            <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0">
              <ReactMarkdown remarkPlugins={[remarkGfm]}>{msg}</ReactMarkdown>
            </div>
          </div>
        )}
        {breakdown && (
          <div className="flex items-center gap-2 text-xs text-gray-500 px-1 flex-wrap">
            {breakdown.prompt !== undefined && breakdown.prompt > 0 && (
              <span className="bg-blue-50 text-blue-700 px-1.5 py-0.5 rounded font-mono">↑ {formatTokens(breakdown.prompt)} prompt</span>
            )}
            {breakdown.tool_input !== undefined && breakdown.tool_input > 0 && (
              <span className="bg-amber-50 text-amber-700 px-1.5 py-0.5 rounded font-mono">→ {formatTokens(breakdown.tool_input)} tool args</span>
            )}
            {breakdown.tool_output !== undefined && breakdown.tool_output > 0 && (
              <span className="bg-amber-50 text-amber-700 px-1.5 py-0.5 rounded font-mono">← {formatTokens(breakdown.tool_output)} tool results</span>
            )}
            {breakdown.total !== undefined && (
              <span className="bg-gray-100 text-gray-700 px-1.5 py-0.5 rounded font-mono">∑ {formatTokens(breakdown.total)} total</span>
            )}
          </div>
        )}
        {(rawMode || !msg) && <JsonBlock data={entry.content} />}
      </div>
    );
  }
  if (entry.type === 'response') {
    return (
      <ResponseExpanded
        entry={entry}
        rawMode={rawMode}
        relatedToolCalls={relatedToolCalls}
      />
    );
  }
  return <div className="pl-7 text-xs text-gray-600">{entry.content}</div>;
}

// Single tool call expanded view.
function ToolCallExpanded({ entry }: { entry: LogEntry }) {
  const { Icon, color, bg } = getToolIcon(entry.tool_name || '');
  const inTok = entry.input_tokens ?? entry.output_tokens;
  return (
    <div className="pl-7 space-y-1">
      <div className="flex items-center gap-2">
        <div className={`shrink-0 w-5 h-5 rounded-full ${bg} flex items-center justify-center`}>
          <Icon size={12} className={color} />
        </div>
        <span className={`text-xs font-mono ${color} font-medium`}>{entry.tool_name}</span>
        {inTok !== undefined && inTok > 0 && (
          <span className="text-xs text-gray-500 font-mono bg-gray-100 px-1.5 py-0.5 rounded">
            ~{formatTokens(inTok)} tok in
          </span>
        )}
      </div>
      <JsonBlock data={entry.content} />
    </div>
  );
}

// Single tool response expanded view. Shows the (possibly truncated)
// response content with the response-side token badge.
function ToolResponseExpanded({ entry }: { entry: LogEntry }) {
  const { Icon, color, bg } = getToolIcon(entry.tool_name || '');
  const outTok = entry.output_tokens;
  return (
    <div className="pl-7 space-y-1">
      <div className="flex items-center gap-2">
        <div className={`shrink-0 w-5 h-5 rounded-full ${bg} flex items-center justify-center`}>
          <Icon size={12} className={color} />
        </div>
        <span className={`text-xs font-mono ${color} font-medium`}>{entry.tool_name}</span>
        <span className="text-xs text-gray-500">response</span>
        {outTok !== undefined && outTok > 0 && (
          <span className="text-xs text-gray-500 font-mono bg-gray-100 px-1.5 py-0.5 rounded">
            ~{formatTokens(outTok)} tok out
          </span>
        )}
      </div>
      <JsonBlock data={entry.content} />
    </div>
  );
}

// Response expanded view: text, reasoning (folded), tool calls (as nested rows).
function ResponseExpanded({ entry, rawMode, relatedToolCalls }: {
  entry: LogEntry;
  rawMode: boolean;
  relatedToolCalls: LogEntry[];
}) {
  const { text, reasoning, toolCalls, tokens } = getAgentMessage(entry.content);
  const [reasoningExpanded, setReasoningExpanded] = useState(false);

  return (
    <div className="pl-7 space-y-2">
      {/* Main text */}
      {text && (
        <div className="bg-indigo-50 border border-indigo-100 rounded-lg px-3 py-2">
          <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
          </div>
        </div>
      )}

      {/* Reasoning — folded by default, expands to show full chain */}
      {reasoning && (
        <div className="border border-purple-100 rounded-lg overflow-hidden">
          <button
            onClick={() => setReasoningExpanded(!reasoningExpanded)}
            className="w-full flex items-center gap-2 px-3 py-1.5 bg-purple-50 hover:bg-purple-100 text-left text-xs"
          >
            <Brain size={12} className="text-purple-600" />
            <span className="font-medium text-purple-700 uppercase tracking-wide">Reasoning</span>
            <span className="text-gray-500 truncate flex-1">
              {reasoning.split('\n').find((l: string) => l.trim().length > 0)?.slice(0, 80)}…
            </span>
            {reasoningExpanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
          </button>
          {reasoningExpanded && (
            <div className="px-3 py-2 bg-white">
              <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0 text-gray-700">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{reasoning}</ReactMarkdown>
              </div>
            </div>
          )}
        </div>
      )}

      {/* Tool calls section - first from parsed response content */}
      {toolCalls.length > 0 && (
        <div className="border border-amber-100 rounded-lg overflow-hidden">
          <div className="px-3 py-1.5 bg-amber-50 text-xs text-amber-700 font-medium uppercase tracking-wide flex items-center gap-2">
            <Wrench size={12} />
            Tool Calls
            <span className="text-amber-500 normal-case">({toolCalls.length})</span>
          </div>
          <div className="divide-y divide-amber-50">
            {toolCalls.map((tc: any, i: number) => (
              <ToolCallRow key={i} toolCall={tc} />
            ))}
          </div>
        </div>
      )}

      {/* Tool calls from separate tool_call entries (engine-logged) */}
      {relatedToolCalls.length > 0 && (
        <div className="border border-amber-100 rounded-lg overflow-hidden">
          <div className="px-3 py-1.5 bg-amber-50 text-xs text-amber-700 font-medium uppercase tracking-wide flex items-center gap-2">
            <Wrench size={12} />
            Tool Calls
            <span className="text-amber-500 normal-case">({relatedToolCalls.length})</span>
          </div>
          <div className="divide-y divide-amber-50">
            {relatedToolCalls.map((tc, i) => (
              <ToolCallRowFromEntry key={i} entry={tc} />
            ))}
          </div>
        </div>
      )}

      {/* Token breakdown */}
      {tokens && (tokens.prompt || tokens.completion || tokens.reasoning) && (
        <div className="flex items-center gap-2 text-xs text-gray-500 px-1 flex-wrap">
          {tokens.prompt !== undefined && <span className="bg-blue-50 text-blue-700 px-1.5 py-0.5 rounded font-mono">↑ {formatTokens(tokens.prompt)} prompt</span>}
          {tokens.reasoning !== undefined && tokens.reasoning > 0 && (
            <span className="bg-purple-50 text-purple-700 px-1.5 py-0.5 rounded font-mono">⊕ {formatTokens(tokens.reasoning)} reasoning</span>
          )}
          {tokens.completion !== undefined && <span className="bg-indigo-50 text-indigo-700 px-1.5 py-0.5 rounded font-mono">↓ {formatTokens(tokens.completion)} completion</span>}
          <span className="bg-gray-100 text-gray-700 px-1.5 py-0.5 rounded font-mono">∑ {formatTokens((tokens.prompt || 0) + (tokens.completion || 0) + (tokens.reasoning || 0))} total</span>
        </div>
      )}

      {rawMode && <JsonBlock data={entry.content} />}
      {!text && toolCalls.length === 0 && relatedToolCalls.length === 0 && !reasoning && !tokens && (
        <JsonBlock data={entry.content} />
      )}
    </div>
  );
}

// Inline tool call row inside a response (parsed from response.content).
function ToolCallRow({ toolCall }: { toolCall: any }) {
  const [expanded, setExpanded] = useState(false);
  const name = toolCall.name || toolCall.function?.name || 'unknown';
  const { Icon, color, bg } = getToolIcon(name);
  const args = toolCall.arguments || toolCall.function?.arguments || {};
  const argsStr = typeof args === 'string' ? args : JSON.stringify(args, null, 2);
  const argBytes = argsStr.length;
  const argTokens = Math.ceil(argBytes / 4);

  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-amber-50 text-left text-xs"
      >
        {expanded ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
        <div className={`shrink-0 w-5 h-5 rounded-full ${bg} flex items-center justify-center`}>
          <Icon size={11} className={color} />
        </div>
        <span className={`font-mono font-medium ${color}`}>{name}</span>
        <span className="text-gray-500 truncate flex-1">
          {args.filePath || args.command || args.pattern || args.status || ''}
        </span>
        <span className="text-xs text-gray-400 font-mono">~{formatTokens(argTokens)} tok</span>
      </button>
      {expanded && (
        <div className="px-3 pb-2">
          <pre className="text-xs font-mono text-gray-700 bg-amber-50/50 rounded p-2 whitespace-pre-wrap break-words border border-amber-100">
            {argsStr}
          </pre>
        </div>
      )}
    </div>
  );
}

// Tool call row from a separate engine-logged entry.
function ToolCallRowFromEntry({ entry }: { entry: LogEntry }) {
  const [expanded, setExpanded] = useState(false);
  const name = entry.tool_name || 'unknown';
  const { Icon, color, bg } = getToolIcon(name);
  const argTokens = entry.input_tokens ?? entry.output_tokens ?? 0;

  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-amber-50 text-left text-xs"
      >
        {expanded ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
        <div className={`shrink-0 w-5 h-5 rounded-full ${bg} flex items-center justify-center`}>
          <Icon size={11} className={color} />
        </div>
        <span className={`font-mono font-medium ${color}`}>{name}</span>
        <span className="text-gray-500 truncate flex-1">
          {getToolCallPreview(entry)}
        </span>
        {argTokens > 0 && (
          <span className="text-xs text-gray-400 font-mono">~{formatTokens(argTokens)} tok</span>
        )}
      </button>
      {expanded && (
        <div className="px-3 pb-2">
          <pre className="text-xs font-mono text-gray-700 bg-amber-50/50 rounded p-2 whitespace-pre-wrap break-words border border-amber-100">
            {entry.content}
          </pre>
        </div>
      )}
    </div>
  );
}

// TokenStatsBar: the compact + expandable token breakdown shown in the
// Run Logs header. Renders a single-line horizontal stacked bar with
// colored segments proportional to each category (prompt/completion/
// reasoning/tool_in/tool_out). Hovering shows a tooltip with the exact
// counts. Clicking the bar expands a more detailed table below.
interface TokenStatsBarProps {
  stats: RunTokenStats | null;
  // messages is used to compute a fallback aggregate when the persisted
  // RunTokenStats hasn't been written yet (e.g. mid-run, before the
  // engine's final log line has flushed).
  messages: LogMessage[];
}

interface TokenSegment {
  key: string;
  label: string;
  value: number;
  color: string;     // bg class for the bar segment
  chip: string;      // full chip class (bg + text) for the expanded legend
}

function TokenStatsBar({ stats, messages }: TokenStatsBarProps) {
  const [expanded, setExpanded] = useState(false);

  // Prefer the persisted run-level aggregate; fall back to summing
  // messages on the fly so the bar still renders something useful while
  // the run is in progress and the final log line hasn't been written.
  const aggregate: RunTokenStats = useMemo(() => {
    if (stats && (stats.prompt_tokens || 0) + (stats.completion_tokens || 0) +
        (stats.reasoning_tokens || 0) + (stats.tool_input_tokens || 0) +
        (stats.tool_output_tokens || 0) > 0) {
      return stats;
    }
    const agg: RunTokenStats = {
      prompt_tokens: 0,
      completion_tokens: 0,
      reasoning_tokens: 0,
      tool_input_tokens: 0,
      tool_output_tokens: 0,
      total_tokens: 0,
    };
    messages?.forEach(m => {
      const e = m.entry;
      if (e.type === 'response') {
        const { tokens } = getAgentMessage(e.content);
        if (tokens?.prompt) agg.prompt_tokens = (agg.prompt_tokens || 0) + tokens.prompt;
        if (tokens?.completion) agg.completion_tokens = (agg.completion_tokens || 0) + tokens.completion;
        if (tokens?.reasoning) agg.reasoning_tokens = (agg.reasoning_tokens || 0) + tokens.reasoning;
      } else if (e.type === 'tool_call') {
        const inT = e.input_tokens ?? e.output_tokens ?? 0;
        agg.tool_input_tokens = (agg.tool_input_tokens || 0) + inT;
      } else if (e.type === 'tool_response') {
        agg.tool_output_tokens = (agg.tool_output_tokens || 0) + (e.output_tokens || 0);
      } else if (e.type === 'request' && e.prompt_tokens) {
        // The proxy overwrites this with the real count once the
        // response comes back, so just track the largest one we see.
        agg.prompt_tokens = Math.max(agg.prompt_tokens || 0, e.prompt_tokens);
      }
    });
    agg.total_tokens = (agg.prompt_tokens || 0) + (agg.completion_tokens || 0) +
      (agg.reasoning_tokens || 0) + (agg.tool_input_tokens || 0) +
      (agg.tool_output_tokens || 0);
    return agg;
  }, [stats, messages]);

  const segments: TokenSegment[] = [
    { key: 'prompt',     label: 'prompt',     value: aggregate.prompt_tokens || 0,     color: 'bg-blue-500',   chip: 'bg-blue-50 text-blue-700' },
    { key: 'reasoning',  label: 'reasoning',  value: aggregate.reasoning_tokens || 0,  color: 'bg-purple-500', chip: 'bg-purple-50 text-purple-700' },
    { key: 'completion', label: 'completion', value: aggregate.completion_tokens || 0, color: 'bg-indigo-500', chip: 'bg-indigo-50 text-indigo-700' },
    { key: 'tool_in',    label: 'tool args',  value: aggregate.tool_input_tokens || 0, color: 'bg-amber-500',  chip: 'bg-amber-50 text-amber-700' },
    { key: 'tool_out',   label: 'tool result',value: aggregate.tool_output_tokens || 0, color: 'bg-amber-300',  chip: 'bg-amber-50 text-amber-600' },
  ];
  const total = segments.reduce((s, x) => s + x.value, 0);
  if (total === 0) return null;

  return (
    <div className="ml-2 flex flex-col gap-1 min-w-0 max-w-md">
      <button
        onClick={() => setExpanded(!expanded)}
        className="group flex items-center gap-2 text-left"
        title="Click for detailed breakdown"
      >
        <div className="flex h-2 w-40 rounded-full overflow-hidden bg-gray-200 shrink-0">
          {segments.map((seg) => {
            const pct = (seg.value / total) * 100;
            if (pct < 0.5) return null;
            return (
              <div
                key={seg.key}
                className={`${seg.color} h-full`}
                style={{ width: `${pct}%` }}
                title={`${seg.label}: ${formatTokens(seg.value)} tok`}
              />
            );
          })}
        </div>
        <span className="text-xs text-gray-700 font-mono whitespace-nowrap">
          {formatTokens(total)} tok
        </span>
        {expanded ? <ChevronUp size={10} className="text-gray-400" /> : <ChevronDown size={10} className="text-gray-400" />}
      </button>
      {expanded && (
        <div className="flex flex-wrap gap-1 text-xs">
          {segments.filter(s => s.value > 0).map((seg) => (
            <span
              key={seg.key}
              className={`${seg.chip} px-1.5 py-0.5 rounded font-mono flex items-center gap-1`}
            >
              <span className={`w-2 h-2 rounded-full ${seg.color}`}></span>
              {formatTokens(seg.value)} {seg.label}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

export const RunLogViewer: React.FC<RunLogViewerProps> = ({ messages, status, autoScroll = true, tokenStats = null }) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const [isAtBottom, setIsAtBottom] = useState(true);
  const [rawMode, setRawMode] = useState(false);

  // Counters for the header
  const counts = useMemo(() => {
    const c = { request: 0, response: 0, tool_call: 0, tool_response: 0, info: 0, error: 0 };
    messages?.forEach(m => {
      if (c[m.entry.type as keyof typeof c] !== undefined) c[m.entry.type as keyof typeof c]++;
    });
    return c;
  }, [messages]);

  useEffect(() => {
    if (autoScroll && isAtBottom && bottomRef.current) {
      bottomRef.current.scrollIntoView({ behavior: 'smooth' });
    }
  }, [messages, autoScroll, isAtBottom]);

  const handleScroll = () => {
    if (!containerRef.current) return;
    const { scrollTop, scrollHeight, clientHeight } = containerRef.current;
    setIsAtBottom(scrollHeight - scrollTop - clientHeight < 50);
  };

  return (
    <div className="flex flex-col h-full">
      {/* Header with status, counters and token stats bar */}
      <div className="flex items-center justify-between px-3 py-2 border-b bg-gray-50 shrink-0 gap-2 flex-wrap">
        <div className="flex items-center gap-2 flex-wrap min-w-0">
          <h3 className="font-bold text-sm text-gray-700">Execution Log</h3>
          {status === 'running' && (
            <span className="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium bg-yellow-100 text-yellow-800 rounded-full">
              <Loader2 size={10} className="animate-spin" />
              Running
            </span>
          )}
          {status === 'completed' && (
            <span className="px-2 py-0.5 text-xs font-medium bg-green-100 text-green-800 rounded-full">Completed</span>
          )}
          {status === 'failed' && (
            <span className="px-2 py-0.5 text-xs font-medium bg-red-100 text-red-800 rounded-full">Failed</span>
          )}
          {messages && messages.length > 0 && (
            <div className="flex items-center gap-1 text-xs text-gray-500">
              {counts.request > 0 && <span className="px-1.5 py-0.5 bg-blue-50 text-blue-700 rounded">{counts.request} req</span>}
              {counts.response > 0 && <span className="px-1.5 py-0.5 bg-indigo-50 text-indigo-700 rounded">{counts.response} res</span>}
              {counts.tool_call > 0 && <span className="px-1.5 py-0.5 bg-amber-50 text-amber-700 rounded">{counts.tool_call} tool</span>}
              {counts.tool_response > 0 && <span className="px-1.5 py-0.5 bg-amber-50 text-amber-600 rounded">{counts.tool_response} result</span>}
            </div>
          )}
          <TokenStatsBar stats={tokenStats} messages={messages || []} />
        </div>
        <button
          onClick={() => setRawMode(!rawMode)}
          className="flex items-center gap-1.5 text-xs text-gray-500 hover:text-gray-700 px-2 py-1 rounded hover:bg-gray-200 transition-colors shrink-0"
          title={rawMode ? 'Switch to formatted view' : 'Switch to raw JSON view'}
        >
          {rawMode ? <FileText size={12} /> : <Code2 size={12} />}
          {rawMode ? 'Formatted' : 'Raw JSON'}
        </button>
      </div>

      {/* Messages list */}
      <div
        ref={containerRef}
        onScroll={handleScroll}
        className="flex-1 overflow-y-auto"
      >
        {!messages || messages.length === 0 ? (
          <div className="flex items-center justify-center text-gray-400 text-sm italic h-full">
            Waiting for logs...
          </div>
        ) : (
          <div>
            {messages.map((msg, idx) => (
              <CollapsibleRow
                key={msg.id || idx}
                entry={msg.entry}
                rawMode={rawMode}
                allEntries={messages}
                index={idx}
              />
            ))}
          </div>
        )}
        <div ref={bottomRef} />
      </div>
    </div>
  );
};
