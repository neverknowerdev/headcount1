/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams, Link } from 'react-router-dom';
import { ArrowLeft } from 'lucide-react';

export const BuiltinAgentDetails: React.FC = () => {
    const { shortName, name } = useParams<{ shortName: string; name: string }>();
    const [agent, setAgent] = useState<any>(null);
    const [notFound, setNotFound] = useState(false);

    useEffect(() => {
        axios.get('/api/agents/builtin')
            .then(res => {
                const found = (res.data || []).find((a: any) => a.name === name);
                if (found) setAgent(found);
                else setNotFound(true);
            })
            .catch(() => setNotFound(true));
    }, [name]);

    if (notFound) {
        return (
            <div className="max-w-2xl">
                <Link to={`/companies/${shortName}/agents`} className="text-gray-500 hover:text-gray-900 inline-flex items-center gap-2 mb-4">
                    <ArrowLeft size={18} /> Back to Agents
                </Link>
                <p className="text-red-600">Built-in agent "{name}" not found.</p>
            </div>
        );
    }

    if (!agent) return <div>Loading...</div>;

    const hasTools = Array.isArray(agent.allowed_tools) && agent.allowed_tools.length > 0;
    const hasMCPs = Array.isArray(agent.allowed_mcps) && agent.allowed_mcps.length > 0;
    const hasSubagents = Array.isArray(agent.subagents) && agent.subagents.length > 0;

    return (
        <div className="max-w-3xl h-full flex flex-col">
            <div className="mb-6 flex items-center space-x-4">
                <Link to={`/companies/${shortName}/agents`} className="text-gray-500 hover:text-gray-900"><ArrowLeft size={20} /></Link>
                <h1 className="text-2xl font-bold">{agent.name}</h1>
                <span className="bg-slate-200 text-slate-700 text-xs px-2 py-1 rounded-full">Built-in — read-only</span>
            </div>

            <div className="flex-1 overflow-y-auto space-y-6">
                {agent.description && (
                    <div className="bg-white p-6 rounded-lg shadow border">
                        <h3 className="font-bold mb-2">Description</h3>
                        <p className="text-sm text-gray-700">{agent.description}</p>
                    </div>
                )}

                <div className="bg-white p-6 rounded-lg shadow border">
                    <h3 className="font-bold mb-4">Configuration</h3>
                    <div className="grid grid-cols-2 gap-4">
                        <div>
                            <p className="text-sm text-gray-500 mb-1">Model</p>
                            <p className="font-medium">
                                {agent.resolved_model
                                    ? `${agent.resolved_model}${agent.resolved_provider ? ` (${agent.resolved_provider})` : ''}`
                                    : "Uses whichever agent this config is assigned to"}
                            </p>
                            {agent.resolved_model && (
                                <p className="text-xs text-gray-400 mt-1">
                                    Configured via{' '}
                                    <Link to={`/companies/${shortName}/providers`} className="text-indigo-600 hover:underline">
                                        Role Model Configuration
                                    </Link>{' '}
                                    on the LLM Providers page.
                                </p>
                            )}
                        </div>
                        <div>
                            <p className="text-sm text-gray-500 mb-1">Reasoning Level</p>
                            <p className="font-medium capitalize">{agent.reasoning_level || 'default'}</p>
                        </div>
                        <div>
                            <p className="text-sm text-gray-500 mb-1">Chat Type</p>
                            <p className="font-medium">{agent.chat_type || 'message_history'}</p>
                        </div>
                        {agent.parent_agent && (
                            <div>
                                <p className="text-sm text-gray-500 mb-1">Parent Agent</p>
                                <p className="font-medium">{agent.parent_agent}</p>
                            </div>
                        )}
                    </div>
                </div>

                <div className="bg-white p-6 rounded-lg shadow border">
                    <h3 className="font-bold mb-4">Tools & MCPs</h3>
                    <div className="space-y-4">
                        <div>
                            <p className="text-sm text-gray-500 mb-2">Allowed Tools</p>
                            {hasTools ? (
                                <div className="flex flex-wrap gap-2">
                                    {agent.allowed_tools.map((t: string) => (
                                        <span key={t} className="bg-indigo-50 text-indigo-700 text-xs font-mono px-2 py-1 rounded border border-indigo-100">{t}</span>
                                    ))}
                                </div>
                            ) : (
                                <p className="text-sm text-gray-600 italic">All tools allowed (no restriction configured).</p>
                            )}
                        </div>
                        <div>
                            <p className="text-sm text-gray-500 mb-2">Allowed MCPs</p>
                            {hasMCPs ? (
                                <div className="flex flex-wrap gap-2">
                                    {agent.allowed_mcps.map((m: string) => (
                                        <span key={m} className="bg-emerald-50 text-emerald-700 text-xs font-mono px-2 py-1 rounded border border-emerald-100">{m}</span>
                                    ))}
                                </div>
                            ) : (
                                <p className="text-sm text-gray-600 italic">All enabled MCPs allowed (no restriction configured).</p>
                            )}
                        </div>
                        {hasSubagents && (
                            <div>
                                <p className="text-sm text-gray-500 mb-2">Can Delegate To</p>
                                <div className="flex flex-wrap gap-2">
                                    {agent.subagents.map((s: string) => (
                                        <span key={s} className="bg-gray-100 text-gray-700 text-xs px-2 py-1 rounded border border-gray-200">{s}</span>
                                    ))}
                                </div>
                            </div>
                        )}
                    </div>
                </div>

                <div className="bg-white p-6 rounded-lg shadow border">
                    <h3 className="font-bold mb-2">System Prompt</h3>
                    <div className="text-xs text-gray-700 bg-gray-50 p-4 rounded border overflow-y-auto max-h-[32rem] whitespace-pre-wrap font-mono">
                        {agent.prompt}
                    </div>
                </div>
            </div>
        </div>
    );
};
