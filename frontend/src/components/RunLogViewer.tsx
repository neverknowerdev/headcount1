import React, { useState, useEffect, useRef, useMemo } from 'react';
import {
  ChevronDown, ChevronRight,
  Bot, User, AlertCircle, Loader2, Code2, FileText,
  FileText as FileIcon, Terminal, Search, ListChecks,
  Wrench, Brain, ChevronUp, MessageSquare, XCircle
} from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

interface TokenUsage {
  prompt?: number;
  completion?: number;
  total?: number;
  reasoning?: number;
  tool_input?: number;
  cached?: number;
}

interface RunTokenStats {
  prompt_tokens?: number;
  completion_tokens?: number;
  reasoning_tokens?: number;
  tool_input_tokens?: number;
  tool_output_tokens?: number;
  cached_tokens?: number;
  total_tokens?: number;
  mcp_tool_tokens?: number;
  mcp_server_tokens?: Record<string, number>;
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

// ─── Hierarchical grouping ────────────────────────────────────────────────────

interface ToolPair {
  call: LogMessage | null;
  response: LogMessage | null;
}

type GroupedItem =
  | { kind: 'in';             key: number; msg: LogMessage }
  | { kind: 'out';            key: number; msg: LogMessage; toolPairs: ToolPair[] }
  | { kind: 'error';          key: number; msg: LogMessage }
  | { kind: 'info';           key: number; msg: LogMessage }
  | { kind: 'system';         key: number; content: string; ts?: string }
  | { kind: 'init-user';      key: number; content: string; ts?: string }
  | { kind: 'init-human';     key: number; content: string; ts?: string }
  | { kind: 'init-assistant'; key: number; content: string; ts?: string };

function groupMessages(messages: LogMessage[]): GroupedItem[] {
  const items: GroupedItem[] = [];
  let key = 0;
  let lastOut: (GroupedItem & { kind: 'out' }) | null = null;
  let firstRequestDone = false;

  for (const msg of messages) {
    switch (msg.entry.type) {
      case 'request': {
        lastOut = null;
        if (!firstRequestDone) {
          firstRequestDone = true;
          // Expand initial context into per-message rows (system, user, assistant).
          let expandedOk = false;
          try {
            const parsed = JSON.parse(msg.entry.content);
            if (parsed.messages && Array.isArray(parsed.messages)) {
              const ts = msg.entry.ts;
              const newItems: GroupedItem[] = [];
              let userMsgCount = 0;
              for (const m of parsed.messages as any[]) {
                if (m.role === 'tool') continue;
                const content = stringifyMessageContent(m.content);
                if (m.role === 'system') {
                  newItems.push({ kind: 'system', key: key++, content, ts });
                } else if (m.role === 'user') {
                  userMsgCount++;
                  if (userMsgCount === 1) {
                    // First user message = task description, sent by agent/orchestrator
                    newItems.push({ kind: 'init-user', key: key++, content, ts });
                  } else {
                    // Subsequent user messages = human comments
                    newItems.push({ kind: 'init-human', key: key++, content, ts });
                  }
                } else if (m.role === 'assistant') {
                  newItems.push({ kind: 'init-assistant', key: key++, content, ts });
                }
              }
              if (newItems.length > 0) {
                items.push(...newItems);
                expandedOk = true;
              }
            }
          } catch {}
          if (expandedOk) break;
        }
        items.push({ kind: 'in', key: key++, msg });
        break;
      }
      case 'response': {
        const outItem: GroupedItem & { kind: 'out' } = {
          kind: 'out', key: key++, msg, toolPairs: [],
        };
        lastOut = outItem;
        items.push(outItem);
        break;
      }
      case 'tool_call': {
        if (lastOut) lastOut.toolPairs.push({ call: msg, response: null });
        break;
      }
      case 'tool_response': {
        if (lastOut) {
          const toolName = msg.entry.tool_name;
          let matched = false;
          for (let i = 0; i < lastOut.toolPairs.length; i++) {
            const pair = lastOut.toolPairs[i];
            if (!pair.response && pair.call?.entry.tool_name === toolName) {
              pair.response = msg;
              matched = true;
              break;
            }
          }
          if (!matched) lastOut.toolPairs.push({ call: null, response: msg });
        }
        break;
      }
      case 'error': {
        lastOut = null;
        items.push({ kind: 'error', key: key++, msg });
        break;
      }
      default: {
        items.push({ kind: 'info', key: key++, msg });
        break;
      }
    }
  }
  return items;
}

// ─── Utility functions ────────────────────────────────────────────────────────

function JsonBlock({ data }: { data: string }) {
  let formatted = data;
  try { formatted = JSON.stringify(JSON.parse(data), null, 2); } catch {}
  return (
    <pre className="p-2 text-xs font-mono text-gray-800 bg-gray-50 rounded overflow-x-auto whitespace-pre-wrap max-h-96 overflow-y-auto border border-gray-200">
      {formatted}
    </pre>
  );
}

const MCP_TOOL_NAMES = new Set(['call_mcp_tool', 'discover_mcp_tool']);

interface ParsedRequestContent {
  latestText: string | null;
  messageCount: number;
  toolResultCount: number;
  toolResults: { name: string; content: string; preview: string }[];
  historyMessageCount: number;
  estimatedTotalTokens: number;
  currentTurnTokens: number;  // tokens for just the new messages in this turn
  toolResultTokens: number;   // estimated tokens from tool results in this turn
  mcpToolTokens: number;      // subset of toolResultTokens from MCP dispatcher tools
}

// Try to extract readable text from a tool result value that may be JSON.
function renderAsText(content: string): string {
  try {
    const parsed = JSON.parse(content);
    if (typeof parsed === 'string') return parsed;
    if (typeof parsed.text === 'string') return parsed.text;
    if (typeof parsed.content === 'string') return parsed.content;
    if (Array.isArray(parsed.content)) {
      const texts = parsed.content
        .filter((b: any) => b.type === 'text')
        .map((b: any) => b.text || '')
        .join('\n');
      if (texts) return texts;
    }
    if (Array.isArray(parsed)) {
      const texts = parsed
        .filter((b: any) => b.type === 'text')
        .map((b: any) => b.text || '')
        .join('\n');
      if (texts) return texts;
    }
  } catch {}
  return content;
}

function stringifyMessageContent(content: unknown): string {
  if (!content) return '';
  if (typeof content === 'string') return content;
  if (Array.isArray(content)) {
    return (content as any[])
      .filter((b: any) => b.type === 'text')
      .map((b: any) => b.text || '')
      .join('\n');
  }
  return JSON.stringify(content);
}

function parseRequestContent(content: string): ParsedRequestContent {
  const empty: ParsedRequestContent = {
    latestText: null, messageCount: 0, toolResultCount: 0, toolResults: [],
    historyMessageCount: 0, estimatedTotalTokens: 0, currentTurnTokens: 0, toolResultTokens: 0, mcpToolTokens: 0,
  };
  try {
    const parsed = JSON.parse(content);
    if (parsed.parts && Array.isArray(parsed.parts) && !parsed.messages) {
      const textParts = parsed.parts
        .filter((p: any) => p.type === 'text' && p.text)
        .map((p: any) => p.text as string);
      return { ...empty, latestText: textParts[textParts.length - 1] || null };
    }
    if (parsed.messages && Array.isArray(parsed.messages)) {
      const msgs: any[] = parsed.messages;
      const estimatedTotalTokens = Math.ceil(JSON.stringify(msgs).length / 4);
      let lastAssistantIdx = -1;
      for (let i = msgs.length - 1; i >= 0; i--) {
        if (msgs[i].role === 'assistant') { lastAssistantIdx = i; break; }
      }
      const historyMessageCount = lastAssistantIdx + 1;
      const currentTurnMsgs = msgs.slice(lastAssistantIdx + 1);
      const currentTurnTokens = Math.ceil(JSON.stringify(currentTurnMsgs).length / 4);

      const toolResults: { name: string; content: string; preview: string }[] = [];
      for (const m of currentTurnMsgs) {
        if (m.role === 'tool') {
          const raw = renderAsText(stringifyMessageContent(m.content));
          toolResults.push({ name: m.name || 'tool', content: raw, preview: raw.length > 120 ? raw.slice(0, 120) + '…' : raw });
        }
        if (m.role === 'user' && Array.isArray(m.content)) {
          for (const block of m.content) {
            if (block.type === 'tool_result') {
              const raw = renderAsText(stringifyMessageContent(block.content));
              toolResults.push({ name: 'tool', content: raw, preview: raw.length > 120 ? raw.slice(0, 120) + '…' : raw });
            }
          }
        }
      }
      const toolResultTokens = Math.ceil(
        toolResults.reduce((sum, tr) => sum + tr.content.length, 0) / 4
      );
      const mcpToolTokens = Math.ceil(
        toolResults.filter(tr => MCP_TOOL_NAMES.has(tr.name))
          .reduce((sum, tr) => sum + tr.content.length, 0) / 4
      );

      // Only look at current-turn messages for user text — not history.
      // This prevents older task messages from showing as the preview on tool-result turns.
      let latestText: string | null = null;
      for (let i = currentTurnMsgs.length - 1; i >= 0; i--) {
        const m = currentTurnMsgs[i];
        if (m.role === 'user') {
          const t = stringifyMessageContent(m.content);
          if (t && !t.startsWith('[')) { latestText = t; break; }
        }
      }
      return { latestText, messageCount: msgs.length, toolResultCount: toolResults.length, toolResults, historyMessageCount, estimatedTotalTokens, currentTurnTokens, toolResultTokens, mcpToolTokens };
    }
    return empty;
  } catch { return empty; }
}

function getAgentMessage(content: string): {
  text: string | null; reasoning: string | null; toolCalls: any[]; tokens: TokenUsage | null;
} {
  try {
    const parsed = JSON.parse(content);
    if (parsed.parts && Array.isArray(parsed.parts)) {
      const textParts = parsed.parts.filter((p: any) => p.type === 'text' && p.text).map((p: any) => p.text);
      const toolCalls = parsed.parts.filter((p: any) => p.type === 'tool_call' || p.tool_call).map((p: any) => p.tool_call || p);
      if (textParts.length > 0 || toolCalls.length > 0) {
        return { text: textParts.join('\n\n') || null, reasoning: null, toolCalls, tokens: null };
      }
    }
    if (parsed.choices && Array.isArray(parsed.choices)) {
      const textParts = parsed.choices.map((c: any) => c.message?.content || c.delta?.content || c.text || '').filter(Boolean);
      const toolCalls = parsed.choices.flatMap((c: any) => c.message?.tool_calls || c.delta?.tool_calls || []);
      if (textParts.length > 0 || toolCalls.length > 0) {
        return { text: textParts.join('\n\n') || null, reasoning: null, toolCalls, tokens: null };
      }
    }
    if (parsed.reasoning || parsed.content || parsed.tool_calls || parsed.tokens) {
      const tokens: TokenUsage = parsed.tokens || null;
      let text = parsed.content || null;
      if (!text && typeof parsed.raw === 'string') {
        try {
          const rawParsed = JSON.parse(parsed.raw);
          if (rawParsed.choices && Array.isArray(rawParsed.choices)) {
            text = rawParsed.choices.map((c: any) => c.message?.content || '').filter(Boolean).join('\n\n') || null;
          }
        } catch {}
      }
      return { text, reasoning: parsed.reasoning || null, toolCalls: Array.isArray(parsed.tool_calls) ? parsed.tool_calls : [], tokens };
    }
    return { text: null, reasoning: null, toolCalls: [], tokens: null };
  } catch { return { text: null, reasoning: null, toolCalls: [], tokens: null }; }
}

function formatTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'K';
  if (n < 1000000) return Math.round(n / 1000) + 'K';
  return (n / 1000000).toFixed(1).replace(/\.0$/, '') + 'M';
}

function formatTime(ts?: string): string {
  if (!ts) return '';
  try { return new Date(ts).toLocaleTimeString('en-GB', { hour12: false }); } catch { return ''; }
}

function getToolIcon(toolName: string): { Icon: any; color: string; bg: string } {
  switch (toolName) {
    case 'read':   return { Icon: FileIcon,    color: 'text-blue-600',   bg: 'bg-blue-100' };
    case 'write':  return { Icon: FileText,    color: 'text-green-600',  bg: 'bg-green-100' };
    case 'edit':   return { Icon: FileText,    color: 'text-orange-600', bg: 'bg-orange-100' };
    case 'bash':   return { Icon: Terminal,    color: 'text-gray-700',   bg: 'bg-gray-200' };
    case 'glob':
    case 'grep':
    case 'rg':     return { Icon: Search,      color: 'text-purple-600', bg: 'bg-purple-100' };
    case 'update_task_status': return { Icon: ListChecks, color: 'text-green-700', bg: 'bg-green-100' };
    case 'todowrite': return { Icon: ListChecks, color: 'text-blue-600', bg: 'bg-blue-100' };
    default:       return { Icon: Wrench,      color: 'text-amber-600',  bg: 'bg-amber-100' };
  }
}

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

// ─── SystemRow: system prompt ─────────────────────────────────────────────────

function SystemRow({ content, ts }: { content: string; ts?: string }) {
  const [expanded, setExpanded] = useState(false);
  const time = formatTime(ts);
  const preview = content.split('\n').find(l => l.trim()) || '';
  const previewShort = preview.length > 80 ? preview.slice(0, 80) + '…' : preview;

  return (
    <div className="border-b border-gray-100">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-2 hover:bg-gray-50 text-left"
      >
        <span className="shrink-0 text-gray-400">
          {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </span>
        <Brain size={13} className="text-purple-500 shrink-0" />
        <span className="shrink-0 text-xs font-semibold text-purple-600">System</span>
        {time && <span className="shrink-0 text-xs text-gray-400 font-mono">{time}</span>}
        <span className="flex-1 truncate text-xs text-gray-400 italic">{previewShort}</span>
      </button>
      {expanded && (
        <div className="px-3 pb-3 pt-1 bg-purple-50/10">
          <pre className="pl-7 text-xs text-gray-700 whitespace-pre-wrap break-words font-mono bg-white border border-purple-100 rounded-lg p-3 max-h-80 overflow-y-auto">
            {content}
          </pre>
        </div>
      )}
    </div>
  );
}

// ─── InitUserRow: user message from initial context (task or human comment) ───

function InitUserRow({ content, ts, rawMode }: { content: string; ts?: string; rawMode: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const time = formatTime(ts);
  const preview = content.split('\n').find(l => l.trim()) || '';
  const previewShort = preview.length > 80 ? preview.slice(0, 80) + '…' : preview;

  return (
    <div className="border-b border-gray-100">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-2 hover:bg-gray-50 text-left"
      >
        <span className="shrink-0 text-gray-400">
          {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </span>
        <User size={13} className="text-blue-500 shrink-0" />
        <span className="shrink-0 text-xs font-semibold text-blue-600">Agent</span>
        {time && <span className="shrink-0 text-xs text-gray-400 font-mono">{time}</span>}
        <span className="flex-1 truncate text-xs text-gray-400 italic">{previewShort}</span>
      </button>
      {expanded && (
        <div className="px-3 pb-3 pt-1 bg-blue-50/20">
          {!rawMode ? (
            <div className="pl-7 bg-white border border-blue-100 rounded-lg px-3 py-2">
              <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{content}</ReactMarkdown>
              </div>
            </div>
          ) : (
            <pre className="pl-7 text-xs text-gray-700 whitespace-pre-wrap break-words">{content}</pre>
          )}
        </div>
      )}
    </div>
  );
}

// ─── InitHumanRow: human comment from initial context ────────────────────────

function InitHumanRow({ content, ts, rawMode }: { content: string; ts?: string; rawMode: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const time = formatTime(ts);
  const preview = content.split('\n').find(l => l.trim()) || '';
  const previewShort = preview.length > 80 ? preview.slice(0, 80) + '…' : preview;

  return (
    <div className="border-b border-gray-100">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-2 hover:bg-gray-50 text-left"
      >
        <span className="shrink-0 text-gray-400">
          {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </span>
        <User size={13} className="text-gray-500 shrink-0" />
        <span className="shrink-0 text-xs font-semibold text-gray-600">User</span>
        {time && <span className="shrink-0 text-xs text-gray-400 font-mono">{time}</span>}
        <span className="flex-1 truncate text-xs text-gray-500 italic">{previewShort}</span>
      </button>
      {expanded && (
        <div className="px-3 pb-3 pt-1 bg-gray-50/50">
          {!rawMode ? (
            <div className="pl-7 bg-white border border-gray-200 rounded-lg px-3 py-2">
              <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{content}</ReactMarkdown>
              </div>
            </div>
          ) : (
            <pre className="pl-7 text-xs text-gray-700 whitespace-pre-wrap break-words">{content}</pre>
          )}
        </div>
      )}
    </div>
  );
}

// ─── InitAssistantRow: assistant message from initial context (AI comment) ────

function InitAssistantRow({ content, ts, rawMode }: { content: string; ts?: string; rawMode: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const time = formatTime(ts);
  const preview = content.split('\n').find(l => l.trim()) || '';
  const previewShort = preview.length > 80 ? preview.slice(0, 80) + '…' : preview;

  return (
    <div className="border-b border-gray-100">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-2 hover:bg-gray-50 text-left"
      >
        <span className="shrink-0 text-gray-400">
          {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </span>
        <Bot size={13} className="text-indigo-500 shrink-0" />
        <span className="shrink-0 text-xs font-semibold text-indigo-600">AI Model</span>
        {time && <span className="shrink-0 text-xs text-gray-400 font-mono">{time}</span>}
        <span className="flex-1 truncate text-xs text-gray-600">{previewShort}</span>
      </button>
      {expanded && (
        <div className="px-3 pb-3 pt-1 bg-indigo-50/10">
          {!rawMode ? (
            <div className="pl-7 bg-indigo-50 border border-indigo-100 rounded-lg px-3 py-2">
              <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{content}</ReactMarkdown>
              </div>
            </div>
          ) : (
            <pre className="pl-7 text-xs text-gray-700 whitespace-pre-wrap break-words">{content}</pre>
          )}
        </div>
      )}
    </div>
  );
}

// ─── InRow: collapsed request (IN) ───────────────────────────────────────────

function InRow({ msg, rawMode }: { msg: LogMessage; rawMode: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const entry = msg.entry;
  const time = formatTime(entry.ts);
  const parsed = useMemo(() => parseRequestContent(entry.content), [entry.content]);
  const promptTokens = entry.prompt_tokens || 0;

  const previewText = useMemo(() => {
    if (parsed.latestText) {
      const line = parsed.latestText.split('\n').find(l => l.trim()) || '';
      return line.length > 80 ? line.slice(0, 80) + '…' : line;
    }
    return null;
  }, [parsed]);

  return (
    <div className="border-b border-gray-100">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-2 hover:bg-gray-50 text-left"
      >
        <span className="shrink-0 text-gray-400">
          {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </span>
        <User size={13} className="text-blue-500 shrink-0" />
        <span className="shrink-0 text-xs font-semibold text-blue-600">Agent</span>
        {time && <span className="shrink-0 text-xs text-gray-400 font-mono">{time}</span>}
        <span className="flex-1 truncate text-xs text-gray-400 italic" title={previewText || ''}>
          {previewText || ''}
        </span>
        {parsed.toolResultCount > 0 && (
          <span className="shrink-0 text-xs text-green-700 bg-green-50 px-1.5 py-0.5 rounded">
            {parsed.toolResultCount} result{parsed.toolResultCount !== 1 ? 's' : ''}
          </span>
        )}
        {(parsed.currentTurnTokens || promptTokens) > 0 && (
          <span className="shrink-0 text-xs text-gray-500 font-mono bg-gray-100 px-1.5 py-0.5 rounded">
            {formatTokens(parsed.currentTurnTokens || promptTokens)} tok
          </span>
        )}
      </button>
      {expanded && (
        <div className="px-3 pb-3 pt-1 bg-blue-50/20 space-y-2">
          {/* Prompt statistics */}
          <div className="flex items-center gap-1.5 text-xs flex-wrap pl-7">
            {parsed.currentTurnTokens > 0 && (
              <span className="bg-blue-50 text-blue-700 px-1.5 py-0.5 rounded font-mono">
                msg: {formatTokens(parsed.currentTurnTokens)} tok
              </span>
            )}
            {parsed.toolResultTokens > 0 && (
              <span className="bg-green-50 text-green-700 px-1.5 py-0.5 rounded font-mono">
                tools: {formatTokens(parsed.toolResultTokens)} tok
              </span>
            )}
            {parsed.mcpToolTokens > 0 && (
              <span className="bg-teal-50 text-teal-700 px-1.5 py-0.5 rounded font-mono">
                🔌 MCP: {formatTokens(parsed.mcpToolTokens)} tok
              </span>
            )}
            {(promptTokens > 0 || parsed.estimatedTotalTokens > 0) && (
              <span className="bg-gray-100 text-gray-600 px-1.5 py-0.5 rounded font-mono">
                full: {formatTokens(promptTokens || parsed.estimatedTotalTokens)} tok
              </span>
            )}
            {parsed.historyMessageCount > 0 && (
              <span className="flex items-center gap-1 bg-gray-100 text-gray-500 px-1.5 py-0.5 rounded">
                <MessageSquare size={10} />
                {parsed.historyMessageCount} prev msgs
              </span>
            )}
          </div>

          {/* Latest user text */}
          {parsed.latestText && !rawMode && (
            <div className="pl-7 bg-white border border-blue-100 rounded-lg px-3 py-2">
              <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{parsed.latestText}</ReactMarkdown>
              </div>
            </div>
          )}

          {/* Tool results */}
          {parsed.toolResults.length > 0 && (
            <div className="pl-7 border border-green-100 rounded-lg overflow-hidden">
              <div className="px-3 py-1.5 bg-green-50 text-xs text-green-700 font-medium flex items-center gap-2">
                <Wrench size={11} /> Tool Results ({parsed.toolResults.length})
              </div>
              <div className="divide-y divide-green-50">
                {parsed.toolResults.map((tr, i) => (
                  <ToolResultRow key={i} name={tr.name} content={tr.content} preview={tr.preview} />
                ))}
              </div>
            </div>
          )}

          {(rawMode || (!parsed.latestText && parsed.toolResults.length === 0)) && (
            <div className="pl-7"><JsonBlock data={entry.content} /></div>
          )}
        </div>
      )}
    </div>
  );
}

// ─── OutRow: collapsed response (OUT) ────────────────────────────────────────

function OutRow({ msg, toolPairs, rawMode }: { msg: LogMessage; toolPairs: ToolPair[]; rawMode: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const entry = msg.entry;
  const time = formatTime(entry.ts);

  const { text, reasoning, toolCalls: parsedToolCalls, tokens } = useMemo(
    () => getAgentMessage(entry.content),
    [entry.content]
  );

  const previewText = useMemo(() => {
    if (text) {
      const line = text.split('\n').find(l => l.trim()) || text;
      return line.length > 80 ? line.slice(0, 80) + '…' : line;
    }
    const names = toolPairs.length > 0
      ? toolPairs.map(p => p.call?.entry.tool_name || p.response?.entry.tool_name).filter(Boolean).join(', ')
      : parsedToolCalls.map((tc: any) => tc.name || tc.function?.name).filter(Boolean).join(', ');
    return names || '(no content)';
  }, [text, toolPairs, parsedToolCalls]);

  const effectiveToolCount = toolPairs.length > 0 ? toolPairs.length : parsedToolCalls.length;
  const totalTokens = tokens
    ? (tokens.prompt || 0) + (tokens.completion || 0) + (tokens.reasoning || 0)
    : 0;

  return (
    <div className="border-b border-gray-100">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-2 hover:bg-gray-50 text-left"
      >
        <span className="shrink-0 text-gray-400">
          {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </span>
        <Bot size={13} className="text-indigo-500 shrink-0" />
        <span className="shrink-0 text-xs font-semibold text-indigo-600">AI Model</span>
        {time && <span className="shrink-0 text-xs text-gray-400 font-mono">{time}</span>}
        <span className="flex-1 truncate text-xs text-gray-600" title={previewText}>
          {previewText}
        </span>
        {effectiveToolCount > 0 && (
          <span className="shrink-0 text-xs text-amber-700 bg-amber-50 px-1.5 py-0.5 rounded">
            {effectiveToolCount} tool{effectiveToolCount !== 1 ? 's' : ''}
          </span>
        )}
        {(tokens?.cached || 0) > 0 && (
          <span className="shrink-0 text-xs text-emerald-700 bg-emerald-50 px-1.5 py-0.5 rounded font-mono">
            ⚡ {formatTokens(tokens!.cached!)}
          </span>
        )}
        {totalTokens > 0 && (
          <span className="shrink-0 text-xs text-gray-500 font-mono bg-gray-100 px-1.5 py-0.5 rounded">
            {formatTokens(totalTokens)} tok
          </span>
        )}
      </button>
      {expanded && (
        <div className="px-3 pb-3 pt-1 bg-indigo-50/10 space-y-2">
          {/* Token breakdown */}
          {tokens && ((tokens.prompt || 0) + (tokens.completion || 0) + (tokens.reasoning || 0)) > 0 && (
            <div className="flex items-center gap-1.5 text-xs flex-wrap pl-7">
              {(tokens.prompt || 0) > 0 && (
                <span className="bg-blue-50 text-blue-700 px-1.5 py-0.5 rounded font-mono" title="Prompt tokens (full context sent to model)">
                  ↑ {formatTokens(tokens.prompt!)} prompt
                </span>
              )}
              {(tokens.cached || 0) > 0 && (
                <span className="bg-emerald-50 text-emerald-700 px-1.5 py-0.5 rounded font-mono" title="Cached prompt tokens (subset of prompt)">
                  ⚡ {formatTokens(tokens.cached!)} cached
                </span>
              )}
              {(tokens.completion || 0) > 0 && (
                <span className="bg-indigo-50 text-indigo-700 px-1.5 py-0.5 rounded font-mono" title="Completion tokens generated">
                  ↓ {formatTokens(tokens.completion!)} completion
                </span>
              )}
              {(tokens.reasoning || 0) > 0 && (
                <span className="bg-purple-100 text-purple-800 px-1.5 py-0.5 rounded font-mono font-semibold" title="Reasoning tokens (chain-of-thought)">
                  ⊕ {formatTokens(tokens.reasoning!)} reasoning
                </span>
              )}
              {totalTokens > 0 && (
                <span className="bg-gray-100 text-gray-700 px-1.5 py-0.5 rounded font-mono">∑ {formatTokens(totalTokens)} total</span>
              )}
            </div>
          )}

          {/* Main agent text */}
          {text && !rawMode && (
            <div className="pl-7 bg-indigo-50 border border-indigo-100 rounded-lg px-3 py-2">
              <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
              </div>
            </div>
          )}

          {/* Reasoning (collapsible) */}
          {reasoning && <div className="pl-7"><ReasoningBlock reasoning={reasoning} /></div>}

          {/* Engine-logged tool pairs (call + response together) */}
          {toolPairs.length > 0 && (
            <div className="pl-7 border border-amber-100 rounded-lg overflow-hidden">
              <div className="px-3 py-1.5 bg-amber-50 text-xs text-amber-700 font-medium flex items-center gap-2">
                <Wrench size={11} /> Tool Calls ({toolPairs.length})
              </div>
              <div className="divide-y divide-amber-50">
                {toolPairs.map((pair, i) => (
                  <ToolPairRow key={i} pair={pair} />
                ))}
              </div>
            </div>
          )}

          {/* Parsed tool calls from response content (fallback) */}
          {toolPairs.length === 0 && parsedToolCalls.length > 0 && (
            <div className="pl-7 border border-amber-100 rounded-lg overflow-hidden">
              <div className="px-3 py-1.5 bg-amber-50 text-xs text-amber-700 font-medium flex items-center gap-2">
                <Wrench size={11} /> Tool Calls ({parsedToolCalls.length})
              </div>
              <div className="divide-y divide-amber-50">
                {parsedToolCalls.map((tc: any, i: number) => (
                  <ToolCallRow key={i} toolCall={tc} />
                ))}
              </div>
            </div>
          )}

          {rawMode && <div className="pl-7"><JsonBlock data={entry.content} /></div>}
          {!text && !reasoning && toolPairs.length === 0 && parsedToolCalls.length === 0 && !rawMode && (
            <div className="pl-7"><JsonBlock data={entry.content} /></div>
          )}
        </div>
      )}
    </div>
  );
}

// ─── ToolPairRow: call + response nested inside OutRow ────────────────────────

function ToolPairRow({ pair }: { pair: ToolPair }) {
  const [expanded, setExpanded] = useState(false);
  const call = pair.call;
  const resp = pair.response;
  const name = call?.entry.tool_name || resp?.entry.tool_name || 'unknown';
  const { Icon, color, bg } = getToolIcon(name);
  const callPreview = call ? getToolCallPreview(call.entry) : '';
  const respFirstLine = resp
    ? (resp.entry.content.split('\n').find(l => l.trim()) || '').slice(0, 60)
    : '';
  const respTokens = resp?.entry.output_tokens || 0;

  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-amber-50/50 text-left text-xs"
      >
        {expanded ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
        <div className={`shrink-0 w-4 h-4 rounded-full ${bg} flex items-center justify-center`}>
          <Icon size={10} className={color} />
        </div>
        <span className={`font-mono font-medium shrink-0 ${color}`}>{name}</span>
        <span className="text-gray-500 truncate flex-1">{callPreview || respFirstLine}</span>
        {respTokens > 0 && (
          <span className="shrink-0 text-xs text-gray-400 font-mono">← {formatTokens(respTokens)} tok</span>
        )}
      </button>
      {expanded && (
        <div className="px-3 pb-2 space-y-1.5">
          {call && (
            <div>
              <div className="text-xs text-amber-600 font-medium mb-0.5 pl-1">Call</div>
              <JsonBlock data={call.entry.content} />
            </div>
          )}
          {resp && (
            <div>
              <div className="text-xs text-green-600 font-medium mb-0.5 pl-1">
                Response{respTokens > 0 ? ` (${formatTokens(respTokens)} tok)` : ''}
              </div>
              <pre className="text-xs text-gray-700 bg-green-50/50 rounded p-2 whitespace-pre-wrap break-words border border-green-100 max-h-60 overflow-y-auto">
                {renderAsText(resp.entry.content)}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ─── ReasoningBlock ───────────────────────────────────────────────────────────

function ReasoningBlock({ reasoning }: { reasoning: string }) {
  const [expanded, setExpanded] = useState(false);
  const firstLine = reasoning.split('\n').find(l => l.trim())?.slice(0, 80) || '';
  return (
    <div className="border border-purple-100 rounded-lg overflow-hidden">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-1.5 bg-purple-50 hover:bg-purple-100 text-left text-xs"
      >
        <Brain size={11} className="text-purple-600 shrink-0" />
        <span className="font-medium text-purple-700 uppercase tracking-wide shrink-0">Reasoning</span>
        <span className="text-gray-500 truncate flex-1">{firstLine}…</span>
        {expanded ? <ChevronUp size={11} /> : <ChevronDown size={11} />}
      </button>
      {expanded && (
        <div className="px-3 py-2 bg-white">
          <div className="prose prose-sm max-w-none prose-headings:mt-2 prose-headings:mb-1 prose-p:my-1 prose-ul:my-1 prose-ol:my-1 prose-li:my-0 text-gray-700">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{reasoning}</ReactMarkdown>
          </div>
        </div>
      )}
    </div>
  );
}

// ─── ErrorRow: always-visible, starts expanded ────────────────────────────────

function ErrorRow({ msg }: { msg: LogMessage }) {
  const [expanded, setExpanded] = useState(true);
  const entry = msg.entry;
  const time = formatTime(entry.ts);
  const summary = entry.content.split('\n').find(l => l.trim()) || entry.content;

  return (
    <div className="border-b border-red-200 bg-red-50">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-2 hover:bg-red-100 text-left"
      >
        <span className="shrink-0 text-red-400">
          {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </span>
        <XCircle size={13} className="text-red-600 shrink-0" />
        <span className="shrink-0 text-xs font-semibold text-red-700">Error</span>
        {time && <span className="shrink-0 text-xs text-red-400 font-mono">{time}</span>}
        <span className="flex-1 truncate text-xs text-red-700 font-medium">{summary}</span>
      </button>
      {expanded && (
        <div className="px-3 pb-3 pt-1 pl-10">
          <pre className="text-xs text-red-800 whitespace-pre-wrap break-words bg-red-100/50 rounded p-2 border border-red-200">
            {entry.content}
          </pre>
        </div>
      )}
    </div>
  );
}

// ─── InfoRow: minimal inline display ─────────────────────────────────────────

function InfoRow({ msg }: { msg: LogMessage }) {
  return (
    <div className="border-b border-gray-50 px-3 py-1 flex items-center gap-2">
      <span className="text-gray-300 text-xs shrink-0">•</span>
      <span className="text-xs text-gray-500 truncate">{msg.entry.content}</span>
      {msg.entry.ts && (
        <span className="text-xs text-gray-300 font-mono shrink-0 ml-auto">{formatTime(msg.entry.ts)}</span>
      )}
    </div>
  );
}

// ─── ToolResultRow (inside InRow expanded) ────────────────────────────────────

function ToolResultRow({ name, content, preview }: { name: string; content: string; preview: string }) {
  const [expanded, setExpanded] = useState(false);
  const { Icon, color, bg } = getToolIcon(name);
  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-green-50 text-left text-xs"
      >
        {expanded ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
        <div className={`shrink-0 w-5 h-5 rounded-full ${bg} flex items-center justify-center`}>
          <Icon size={11} className={color} />
        </div>
        <span className={`font-mono font-medium ${color}`}>{name}</span>
        <span className="text-gray-500 truncate flex-1">{preview}</span>
      </button>
      {expanded && (
        <div className="px-3 pb-2">
          <pre className="text-xs text-gray-700 bg-green-50/50 rounded p-2 whitespace-pre-wrap break-words border border-green-100 max-h-60 overflow-y-auto">
            {renderAsText(content)}
          </pre>
        </div>
      )}
    </div>
  );
}

// ─── ToolCallRow (inside OutRow expanded, parsed from response content) ───────

function ToolCallRow({ toolCall }: { toolCall: any }) {
  const [expanded, setExpanded] = useState(false);
  const name = toolCall.name || toolCall.function?.name || 'unknown';
  const { Icon, color, bg } = getToolIcon(name);
  const args = toolCall.arguments || toolCall.function?.arguments || {};
  const argsStr = typeof args === 'string' ? args : JSON.stringify(args, null, 2);

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
          {(args as any).filePath || (args as any).command || (args as any).pattern || (args as any).status || ''}
        </span>
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

// ─── TokenStatsBar ────────────────────────────────────────────────────────────

interface TokenStatsBarProps {
  stats: RunTokenStats | null;
  messages: LogMessage[];
}

interface TokenSegment {
  key: string;
  label: string;
  value: number;
  color: string;
  chip: string;
  isSub?: boolean;
}

function TokenStatsBar({ stats, messages }: TokenStatsBarProps) {
  const [expanded, setExpanded] = useState(false);

  const aggregate: RunTokenStats = useMemo(() => {
    if (stats && (stats.prompt_tokens || 0) + (stats.completion_tokens || 0) +
        (stats.reasoning_tokens || 0) + (stats.tool_input_tokens || 0) +
        (stats.tool_output_tokens || 0) > 0) {
      return stats;
    }
    const agg: RunTokenStats = {
      prompt_tokens: 0, completion_tokens: 0, reasoning_tokens: 0,
      tool_input_tokens: 0, tool_output_tokens: 0, cached_tokens: 0, total_tokens: 0,
    };
    messages?.forEach(m => {
      const e = m.entry;
      if (e.type === 'response') {
        const { tokens } = getAgentMessage(e.content);
        if (tokens?.prompt)     agg.prompt_tokens     = (agg.prompt_tokens || 0)     + tokens.prompt;
        if (tokens?.completion) agg.completion_tokens = (agg.completion_tokens || 0) + tokens.completion;
        if (tokens?.reasoning)  agg.reasoning_tokens  = (agg.reasoning_tokens || 0)  + tokens.reasoning;
        if (tokens?.cached)     agg.cached_tokens     = (agg.cached_tokens || 0)     + tokens.cached;
      } else if (e.type === 'tool_call') {
        agg.tool_input_tokens = (agg.tool_input_tokens || 0) + (e.input_tokens ?? e.output_tokens ?? 0);
      } else if (e.type === 'tool_response') {
        agg.tool_output_tokens = (agg.tool_output_tokens || 0) + (e.output_tokens || 0);
        if (e.tool_name && MCP_TOOL_NAMES.has(e.tool_name)) {
          agg.mcp_tool_tokens = (agg.mcp_tool_tokens || 0) + (e.output_tokens || 0);
        }
      } else if (e.type === 'request' && e.prompt_tokens) {
        agg.prompt_tokens = Math.max(agg.prompt_tokens || 0, e.prompt_tokens);
      }
    });
    agg.total_tokens = (agg.prompt_tokens || 0) + (agg.completion_tokens || 0) +
      (agg.reasoning_tokens || 0) + (agg.tool_input_tokens || 0) + (agg.tool_output_tokens || 0);
    return agg;
  }, [stats, messages]);

  const barSegments: TokenSegment[] = [
    { key: 'prompt',     label: 'prompt',      value: aggregate.prompt_tokens     || 0, color: 'bg-blue-500',   chip: 'bg-blue-50 text-blue-700' },
    { key: 'reasoning',  label: 'reasoning',   value: aggregate.reasoning_tokens  || 0, color: 'bg-purple-500', chip: 'bg-purple-50 text-purple-700' },
    { key: 'completion', label: 'completion',  value: aggregate.completion_tokens || 0, color: 'bg-indigo-500', chip: 'bg-indigo-50 text-indigo-700' },
    { key: 'tool_in',    label: 'tool args',   value: aggregate.tool_input_tokens  || 0, color: 'bg-amber-500',  chip: 'bg-amber-50 text-amber-700' },
    { key: 'tool_out',   label: 'tool result', value: aggregate.tool_output_tokens || 0, color: 'bg-amber-300',  chip: 'bg-amber-50 text-amber-600' },
  ];
  const cachedSegment: TokenSegment = {
    key: 'cached', label: 'cached (of prompt)', value: aggregate.cached_tokens || 0,
    color: 'bg-emerald-500', chip: 'bg-emerald-50 text-emerald-700', isSub: true,
  };

  const total = barSegments.reduce((s, x) => s + x.value, 0);
  if (total === 0) return null;

  return (
    <div className="ml-2 flex flex-col gap-1 min-w-0 max-w-md">
      <button
        onClick={() => setExpanded(!expanded)}
        className="group flex items-center gap-2 text-left"
        title="Click for detailed breakdown"
      >
        <div className="flex h-2 w-40 rounded-full overflow-hidden bg-gray-200 shrink-0">
          {barSegments.map(seg => {
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
        <span className="text-xs text-gray-700 font-mono whitespace-nowrap">{formatTokens(total)} tok</span>
        {(aggregate.mcp_tool_tokens || 0) > 0 && (
          <span className="text-xs text-teal-700 font-mono whitespace-nowrap bg-teal-50 px-1.5 py-0.5 rounded">
            🔌 {formatTokens(aggregate.mcp_tool_tokens!)} MCP
          </span>
        )}
        {expanded ? <ChevronUp size={10} className="text-gray-400" /> : <ChevronDown size={10} className="text-gray-400" />}
      </button>
      {expanded && (
        <div className="flex flex-wrap gap-1 text-xs">
          {barSegments.filter(s => s.value > 0).map(seg => (
            <span key={seg.key} className={`${seg.chip} px-1.5 py-0.5 rounded font-mono flex items-center gap-1`}>
              <span className={`w-2 h-2 rounded-full ${seg.color}`}></span>
              {formatTokens(seg.value)} {seg.label}
            </span>
          ))}
          {cachedSegment.value > 0 && (
            <span className={`${cachedSegment.chip} px-1.5 py-0.5 rounded font-mono flex items-center gap-1`}>
              <span className={`w-2 h-2 rounded-full ${cachedSegment.color}`}></span>
              ⚡ {formatTokens(cachedSegment.value)} {cachedSegment.label}
            </span>
          )}
          {(aggregate.mcp_tool_tokens || 0) > 0 && (
            <div className="w-full flex flex-wrap gap-1 pt-0.5 border-t border-gray-100 mt-0.5">
              <span className="bg-teal-50 text-teal-700 px-1.5 py-0.5 rounded font-mono flex items-center gap-1">
                🔌 {formatTokens(aggregate.mcp_tool_tokens!)} MCP total
              </span>
              {Object.entries(stats?.mcp_server_tokens ?? aggregate.mcp_server_tokens ?? {})
                .sort((a, b) => b[1] - a[1])
                .map(([server, count]) => (
                  <span key={server} className="bg-teal-50/60 text-teal-600 px-1.5 py-0.5 rounded font-mono">
                    {server}: {formatTokens(count)}
                  </span>
                ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ─── Main RunLogViewer ────────────────────────────────────────────────────────

export const RunLogViewer: React.FC<RunLogViewerProps> = ({ messages, status, autoScroll = true, tokenStats = null }) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const [isAtBottom, setIsAtBottom] = useState(true);
  const [rawMode, setRawMode] = useState(false);

  const grouped = useMemo(() => groupMessages(messages || []), [messages]);

  const counts = useMemo(() => {
    let req = 0, res = 0, tools = 0;
    grouped.forEach(item => {
      if (item.kind === 'in') req++;
      else if (item.kind === 'out') { res++; tools += item.toolPairs.length; }
    });
    return { req, res, tools };
  }, [grouped]);

  const hasErrors = useMemo(
    () => grouped.some(item => item.kind === 'error'),
    [grouped]
  );

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
      {/* Header */}
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
          {grouped.length > 0 && (
            <div className="flex items-center gap-1 text-xs text-gray-500">
              {counts.req  > 0 && <span className="px-1.5 py-0.5 bg-blue-50   text-blue-700   rounded">{counts.req} in</span>}
              {counts.res  > 0 && <span className="px-1.5 py-0.5 bg-indigo-50 text-indigo-700 rounded">{counts.res} out</span>}
              {counts.tools > 0 && <span className="px-1.5 py-0.5 bg-amber-50  text-amber-700  rounded">{counts.tools} tools</span>}
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

      {/* Failed with no captured logs */}
      {status === 'failed' && !hasErrors && (!messages || messages.length === 0) && (
        <div className="px-3 py-2 bg-red-50 border-b border-red-200 flex items-start gap-2 shrink-0">
          <AlertCircle size={14} className="text-red-600 mt-0.5 shrink-0" />
          <div className="text-xs">
            <div className="font-medium text-red-700">Run failed</div>
            <div className="text-red-600">No execution log was captured. Check the server logs for details.</div>
          </div>
        </div>
      )}

      {/* Messages */}
      <div
        ref={containerRef}
        onScroll={handleScroll}
        className="flex-1 overflow-y-auto"
      >
        {grouped.length === 0 ? (
          <div className="flex items-center justify-center text-gray-400 text-sm italic h-full">
            {status === 'failed' ? 'No log entries captured.' : 'Waiting for logs...'}
          </div>
        ) : (
          <div>
            {grouped.map(item => {
              if (item.kind === 'in')             return <InRow            key={item.key} msg={item.msg} rawMode={rawMode} />;
              if (item.kind === 'out')            return <OutRow           key={item.key} msg={item.msg} toolPairs={item.toolPairs} rawMode={rawMode} />;
              if (item.kind === 'system')         return <SystemRow        key={item.key} content={item.content} ts={item.ts} />;
              if (item.kind === 'init-user')      return <InitUserRow      key={item.key} content={item.content} ts={item.ts} rawMode={rawMode} />;
              if (item.kind === 'init-human')     return <InitHumanRow     key={item.key} content={item.content} ts={item.ts} rawMode={rawMode} />;
              if (item.kind === 'init-assistant') return <InitAssistantRow key={item.key} content={item.content} ts={item.ts} rawMode={rawMode} />;
              if (item.kind === 'error')          return <ErrorRow         key={item.key} msg={item.msg} />;
              return <InfoRow key={item.key} msg={item.msg} />;
            })}
          </div>
        )}
        <div ref={bottomRef} />
      </div>
    </div>
  );
};
