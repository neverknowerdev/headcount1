import React, { useState } from 'react';
import { ChevronDown, ChevronRight, Info, AlertTriangle, XCircle, Send, Download, Wrench } from 'lucide-react';

interface LogEntryData {
    id: string;
    timestamp: string;
    type: 'info' | 'error' | 'warning' | 'request' | 'response' | 'tool_call' | 'tool_result';
    content: string;
    fullContent?: string;
    metadata?: {
        model?: string;
        provider?: string;
        toolName?: string;
        toolParams?: Record<string, any>;
        responseSize?: number;
        requestLen?: number;
        responseLen?: number;
        toolCalls?: number;
    };
}

interface LogEntryProps {
    entry: LogEntryData;
    showRaw?: boolean;
}

const typeConfig: Record<string, { icon: React.ReactNode; bgClass: string; borderClass: string; textClass: string }> = {
    info: {
        icon: <Info size={16} />,
        bgClass: 'bg-blue-50',
        borderClass: 'border-blue-200',
        textClass: 'text-blue-800',
    },
    error: {
        icon: <XCircle size={16} />,
        bgClass: 'bg-red-50',
        borderClass: 'border-red-200',
        textClass: 'text-red-800',
    },
    warning: {
        icon: <AlertTriangle size={16} />,
        bgClass: 'bg-yellow-50',
        borderClass: 'border-yellow-200',
        textClass: 'text-yellow-800',
    },
    request: {
        icon: <Send size={16} />,
        bgClass: 'bg-purple-50',
        borderClass: 'border-purple-200',
        textClass: 'text-purple-800',
    },
    response: {
        icon: <Download size={16} />,
        bgClass: 'bg-green-50',
        borderClass: 'border-green-200',
        textClass: 'text-green-800',
    },
    tool_call: {
        icon: <Wrench size={16} />,
        bgClass: 'bg-orange-50',
        borderClass: 'border-orange-200',
        textClass: 'text-orange-800',
    },
    tool_result: {
        icon: <Wrench size={16} />,
        bgClass: 'bg-orange-50',
        borderClass: 'border-orange-200',
        textClass: 'text-orange-800',
    },
};

function formatTimestamp(ts: string): string {
    try {
        const date = new Date(ts);
        return date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    } catch {
        return ts;
    }
}

function formatBytes(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function extractRequestPreview(content: string): string {
    // Handle old format: "Request body: {JSON...}"
    let jsonContent = content;
    if (content.startsWith('Request body: ') || content.startsWith('Request: ')) {
        const jsonStart = content.indexOf('{');
        if (jsonStart !== -1) {
            jsonContent = content.substring(jsonStart);
        }
    }
    
    try {
        const parsed = JSON.parse(jsonContent);
        const parts = [];
        
        // Extract the user message text
        if (parsed.parts && Array.isArray(parsed.parts)) {
            const textPart = parsed.parts.find((p: any) => p.type === 'text');
            if (textPart?.text) {
                const text = textPart.text;
                parts.push(`"${text.substring(0, 120)}${text.length > 120 ? '...' : ''}"`);
            }
        }
        
        // Extract model info
        if (parsed.model) {
            const modelName = parsed.model.modelID || parsed.model;
            parts.push(`Model: ${modelName}`);
        }
        
        return parts.join(' · ') || content.substring(0, 150) + '...';
    } catch {
        return content.substring(0, 150) + (content.length > 150 ? '...' : '');
    }
}

function extractResponsePreview(content: string): string {
    // Handle old format: "Response body: {JSON...}"
    let jsonContent = content;
    if (content.startsWith('Response body: ') || content.startsWith('Response: ')) {
        const jsonStart = content.indexOf('{');
        if (jsonStart !== -1) {
            jsonContent = content.substring(jsonStart);
        }
    }
    
    // If content is already plain text (not JSON), show it directly
    if (!jsonContent.startsWith('{') && !jsonContent.startsWith('[')) {
        return `"${jsonContent.substring(0, 150)}${jsonContent.length > 150 ? '...' : ''}"`;
    }
    
    try {
        const parsed = JSON.parse(jsonContent);
        
        // Try to extract text from parts array
        if (parsed.parts && Array.isArray(parsed.parts)) {
            const textParts = parsed.parts.filter((p: any) => p.type === 'text' && p.text);
            if (textParts.length > 0) {
                const text = textParts.map((p: any) => p.text).join(' ');
                return `"${text.substring(0, 150)}${text.length > 150 ? '...' : ''}"`;
            }
        }
        
        // If it's a string value
        if (typeof parsed === 'string') {
            return `"${parsed.substring(0, 150)}${parsed.length > 150 ? '...' : ''}"`;
        }
        
        return jsonContent.substring(0, 150) + '...';
    } catch {
        return `"${jsonContent.substring(0, 150)}${jsonContent.length > 150 ? '...' : ''}"`;
    }
}

function formatRequestContent(content: string): string {
    // Handle old format: "Request body: {JSON...}"
    let jsonContent = content;
    if (content.startsWith('Request body: ') || content.startsWith('Request: ')) {
        const jsonStart = content.indexOf('{');
        if (jsonStart !== -1) {
            jsonContent = content.substring(jsonStart);
        }
    }
    
    try {
        const parsed = JSON.parse(jsonContent);
        const lines: string[] = [];
        
        if (parsed.model) {
            const modelName = parsed.model.modelID || parsed.model;
            lines.push(`Model: ${modelName}`);
            if (parsed.model.providerID) {
                lines.push(`Provider: ${parsed.model.providerID}`);
            }
        }
        
        if (parsed.system) {
            const systemPrompt = parsed.system;
            lines.push(`\nSystem Prompt (${systemPrompt.length} chars):`);
            lines.push(systemPrompt.substring(0, 300) + (systemPrompt.length > 300 ? '...' : ''));
        }
        
        if (parsed.parts && Array.isArray(parsed.parts)) {
            const textPart = parsed.parts.find((p: any) => p.type === 'text');
            if (textPart?.text) {
                lines.push(`\nUser Message:`);
                lines.push(textPart.text);
            }
        }
        
        return lines.join('\n') || content;
    } catch {
        return content;
    }
}

function formatResponseContent(content: string): string {
    // Handle old format: "Response body: {JSON...}"
    let jsonContent = content;
    if (content.startsWith('Response body: ') || content.startsWith('Response: ')) {
        const jsonStart = content.indexOf('{');
        if (jsonStart !== -1) {
            jsonContent = content.substring(jsonStart);
        }
    }
    
    // If content is already plain text (not JSON), return as-is
    if (!jsonContent.startsWith('{') && !jsonContent.startsWith('[')) {
        return jsonContent;
    }
    
    try {
        const parsed = JSON.parse(jsonContent);
        
        if (parsed.parts && Array.isArray(parsed.parts)) {
            const textParts = parsed.parts.filter((p: any) => p.type === 'text' && p.text);
            if (textParts.length > 0) {
                return textParts.map((p: any) => p.text).join('\n');
            }
        }
        
        if (typeof parsed === 'string') {
            return parsed;
        }
        
        return jsonContent;
    } catch {
        return jsonContent;
    }
}

export const LogEntry: React.FC<LogEntryProps> = ({ entry, showRaw = false }) => {
    const [isExpanded, setIsExpanded] = useState(false);
    const config = typeConfig[entry.type] || typeConfig.info;
    const hasExpandableContent = entry.fullContent || entry.metadata?.toolParams;

    const renderToolParams = (params: Record<string, any>) => {
        return Object.entries(params).map(([key, value]) => (
            <div key={key} className="flex">
                <span className="text-gray-500 mr-2 min-w-[100px]">{key}:</span>
                <span className="text-gray-900 break-all">
                    {typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value)}
                </span>
            </div>
        ));
    };

    const getPreviewText = () => {
        if (showRaw) return entry.content;
        if (entry.type === 'request') {
            return extractRequestPreview(entry.fullContent || entry.content);
        }
        if (entry.type === 'response') {
            return extractResponsePreview(entry.fullContent || entry.content);
        }
        return entry.content;
    };

    const getFormattedContent = () => {
        if (entry.type === 'request') {
            return formatRequestContent(entry.fullContent || entry.content);
        }
        if (entry.type === 'response') {
            return formatResponseContent(entry.fullContent || entry.content);
        }
        return entry.fullContent || entry.content;
    };

    return (
        <div className={`${config.bgClass} ${config.borderClass} border rounded-lg mb-2 overflow-hidden transition-all duration-200`}>
            <div 
                className={`px-4 py-3 flex items-center gap-3 cursor-pointer ${hasExpandableContent ? 'hover:bg-opacity-80' : ''}`}
                onClick={() => hasExpandableContent && setIsExpanded(!isExpanded)}
            >
                <span className={`${config.textClass} flex-shrink-0`}>
                    {config.icon}
                </span>

                <span className="text-gray-400 text-xs font-mono flex-shrink-0">
                    {formatTimestamp(entry.timestamp)}
                </span>

                <div className="flex-1 min-w-0">
                    <span className={`${config.textClass} font-medium text-sm`}>
                        {entry.type === 'request' || entry.type === 'response' ? (
                            <span className="text-gray-700 font-normal italic">
                                {getPreviewText()}
                            </span>
                        ) : (
                            entry.content
                        )}
                    </span>
                    
                    {entry.metadata && (
                        <div className="flex gap-2 mt-1 flex-wrap">
                            {entry.metadata.model && (
                                <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-700">
                                    {entry.metadata.model}
                                </span>
                            )}
                            {entry.metadata.toolName && (
                                <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-orange-100 text-orange-700">
                                    {entry.metadata.toolName}
                                </span>
                            )}
                            {entry.metadata.responseSize !== undefined && entry.metadata.responseSize > 0 && (
                                <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-600">
                                    Response: {formatBytes(entry.metadata.responseSize)}
                                </span>
                            )}
                            {entry.metadata.requestLen !== undefined && (
                                <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-600">
                                    {formatBytes(entry.metadata.requestLen)}
                                </span>
                            )}
                            {entry.metadata.responseLen !== undefined && (
                                <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-600">
                                    {entry.metadata.responseLen} chars
                                </span>
                            )}
                            {entry.metadata.toolCalls !== undefined && entry.metadata.toolCalls > 0 && (
                                <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-orange-100 text-orange-700">
                                    {entry.metadata.toolCalls} tool call{entry.metadata.toolCalls > 1 ? 's' : ''}
                                </span>
                            )}
                        </div>
                    )}
                </div>

                {hasExpandableContent && (
                    <span className="text-gray-400 flex-shrink-0">
                        {isExpanded ? <ChevronDown size={18} /> : <ChevronRight size={18} />}
                    </span>
                )}
            </div>

            {isExpanded && hasExpandableContent && (
                <div className="px-4 pb-4 border-t border-gray-200 bg-white">
                    {entry.type === 'tool_call' && entry.metadata?.toolParams && (
                        <div className="mt-3">
                            <h4 className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">Parameters</h4>
                            <div className="bg-gray-50 rounded p-3 text-sm font-mono">
                                {renderToolParams(entry.metadata.toolParams)}
                            </div>
                        </div>
                    )}

                    {entry.fullContent && (
                        <div className="mt-3">
                            <h4 className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">
                                {entry.type === 'request' ? 'Full Request' : entry.type === 'response' ? 'Full Response' : 'Full Content'}
                            </h4>
                            <pre className="bg-gray-900 text-green-400 rounded p-3 text-xs overflow-x-auto max-h-96 overflow-y-auto whitespace-pre-wrap">
                                {showRaw ? entry.fullContent : getFormattedContent()}
                            </pre>
                        </div>
                    )}

                    {entry.type === 'tool_call' && entry.metadata?.responseSize !== undefined && entry.metadata.responseSize > 0 && (
                        <div className="mt-3">
                            <h4 className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">Response</h4>
                            <div className="bg-gray-50 rounded p-3 text-sm text-gray-600">
                                Response size: {formatBytes(entry.metadata.responseSize)}
                            </div>
                        </div>
                    )}
                </div>
            )}
        </div>
    );
};

export const LegacyLogBlock: React.FC<{ content: string; showRaw?: boolean }> = ({ content, showRaw = false }) => {
    const [isExpanded, setIsExpanded] = useState(false);
    
    const formatLegacyContent = (text: string) => {
        if (showRaw) return text;
        
        const lines = text.split('\n');
        const formatted: string[] = [];
        
        for (const line of lines) {
            if (line.includes('{') && line.includes('}')) {
                const jsonStart = line.indexOf('{');
                const jsonEnd = line.lastIndexOf('}') + 1;
                if (jsonStart !== -1 && jsonEnd > jsonStart) {
                    const prefix = line.substring(0, jsonStart);
                    const jsonStr = line.substring(jsonStart, jsonEnd);
                    
                    try {
                        JSON.parse(jsonStr);
                        
                        if (prefix.includes('Request body:') || prefix.includes('Request:')) {
                            formatted.push(`${prefix}${formatRequestContent(jsonStr)}`);
                        } else if (prefix.includes('Response body:') || prefix.includes('Response:')) {
                            formatted.push(`${prefix}${formatResponseContent(jsonStr)}`);
                        } else {
                            formatted.push(line);
                        }
                    } catch {
                        formatted.push(line);
                    }
                } else {
                    formatted.push(line);
                }
            } else {
                formatted.push(line);
            }
        }
        
        return formatted.join('\n');
    };
    
    const preview = content.substring(0, 200) + (content.length > 200 ? '...' : '');
    
    return (
        <div className="bg-gray-50 border border-gray-200 rounded-lg mb-2 overflow-hidden">
            <div 
                className="px-4 py-3 flex items-center gap-3 cursor-pointer hover:bg-gray-100"
                onClick={() => setIsExpanded(!isExpanded)}
            >
                <span className="text-gray-400 text-xs font-mono">
                    Legacy Log
                </span>
                <span className="text-gray-600 text-sm flex-1">
                    {preview}
                </span>
                <span className="text-gray-400 flex-shrink-0">
                    {isExpanded ? <ChevronDown size={18} /> : <ChevronRight size={18} />}
                </span>
            </div>
            
            {isExpanded && (
                <div className="px-4 pb-4 border-t border-gray-200 bg-white">
                    <pre className="bg-gray-900 text-green-400 rounded p-3 text-xs overflow-x-auto max-h-96 overflow-y-auto whitespace-pre-wrap">
                        {formatLegacyContent(content)}
                    </pre>
                </div>
            )}
        </div>
    );
};
