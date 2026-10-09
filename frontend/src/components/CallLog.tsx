import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { X } from 'lucide-react';
import { RunLogViewer } from './RunLogViewer';
import type { LogMessage } from './RunLogViewer';
import { errorMessage, formatDateTime, formatDuration, formatTokens, phaseLabel, readSmartExchange } from '../lib/workflow';
import type { LLMCall, TaskStep } from '../lib/workflow';

const Block: React.FC<{ title: string; children: React.ReactNode; testId?: string }> = ({ title, children, testId }) => (
    <div>
        <p className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-gray-400">{title}</p>
        <pre data-testid={testId} className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded border bg-gray-50 p-3 text-xs text-gray-800">{children}</pre>
    </div>
);

// SmartExchange shows a smart model's prompt and its answer in full.
export const SmartExchange: React.FC<{ prompt: string; response: string; error?: string }> = ({ prompt, response, error }) => {
    const view = readSmartExchange(prompt, response);
    return (
        <div className="space-y-3">
            {view.system && <Block title="Who it is and how the workflow works" testId="smart-system">{view.system}</Block>}
            <Block title="What it was asked" testId="smart-prompt">{view.user || '(empty)'}</Block>
            {view.tools.length > 0 && (
                <p className="text-xs text-gray-500">It could answer with: <span className="font-mono">{view.tools.join(', ')}</span></p>
            )}
            {view.answerTool && <Block title={`Its answer: ${view.answerTool}`} testId="smart-answer">{view.answerArguments}</Block>}
            {view.answerText && <Block title="What it said">{view.answerText}</Block>}
            {!view.answerTool && !view.answerText && !error && <p className="text-xs italic text-gray-400">No answer was received.</p>}
            {error && <div className="rounded border border-red-200 bg-red-50 p-3 text-xs text-red-800">{error}</div>}
        </div>
    );
};

interface CallDetails {
    call: LLMCall;
    step?: TaskStep;
    entries?: Record<string, unknown>[];
}

// CallLog opens one model call as a log: the full prompt and answer of a smart
// call, or the stretch of an executor session that one call covers. Give it a
// key of the call's id so opening another call starts from a clean slate.
export const CallLog: React.FC<{ callId: number; onClose: () => void }> = ({ callId, onClose }) => {
    const [details, setDetails] = useState<CallDetails | null>(null);
    const [error, setError] = useState<string | null>(null);

    useEffect(() => {
        let current = true;
        axios.get(`/api/usage/calls/${callId}`)
            .then(res => { if (current) setDetails(res.data); })
            .catch(e => { if (current) setError(errorMessage(e, 'Could not load the call')); });
        return () => { current = false; };
    }, [callId]);

    const call = details?.call;
    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onClose}>
            <div data-testid="call-log" className="flex max-h-[90vh] w-full max-w-4xl flex-col rounded-lg bg-white shadow-xl" onClick={e => e.stopPropagation()}>
                <div className="flex items-start justify-between border-b px-5 py-3">
                    <div>
                        <h3 className="font-semibold text-gray-900">
                            {call ? `${call.tier === 'smart' ? 'Smart' : call.tier === 'cheap' ? 'Executor' : call.tier} call · ${call.model || call.requested_model || 'unknown model'}` : 'Model call'}
                        </h3>
                        {call && (
                            <p className="mt-0.5 text-xs text-gray-500">
                                {call.agent_name || 'No agent'} · {phaseLabel(call.phase)} · {formatDateTime(call.created_at)} · {formatDuration(call.duration_ms)} ·{' '}
                                {formatTokens(call.prompt_tokens)} in / {formatTokens(call.completion_tokens)} out
                                {call.status !== 'ok' && <span className="ml-1 font-medium text-red-600">· {call.status}</span>}
                            </p>
                        )}
                    </div>
                    <button onClick={onClose} aria-label="Close" className="text-gray-400 hover:text-gray-700"><X size={18} /></button>
                </div>
                <div className="flex-1 overflow-y-auto p-5">
                    {error && <div className="rounded border border-red-200 bg-red-50 p-3 text-sm text-red-800">{error}</div>}
                    {!details && !error && <p className="text-sm text-gray-500">Loading…</p>}
                    {details?.step && <SmartExchange prompt={details.step.prompt} response={details.step.response} error={details.call.error || details.step.error} />}
                    {details && !details.step && (details.entries?.length ? (
                        <RunLogViewer messages={details.entries.map((entry, id) => ({ id, entry })) as unknown as LogMessage[]} autoScroll={false} compact agentName={details.call.agent_name} />
                    ) : (
                        <div className="space-y-2">
                            <p className="text-sm text-gray-500">This call has no log of its own.</p>
                            {details.call.error && <div className="rounded border border-red-200 bg-red-50 p-3 text-xs text-red-800">{details.call.error}</div>}
                        </div>
                    ))}
                </div>
            </div>
        </div>
    );
};
