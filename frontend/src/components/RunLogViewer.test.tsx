import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { RunLogViewer } from './RunLogViewer';

function message(id: number, entry: Record<string, unknown>): any {
    return { id, entry };
}

describe('RunLogViewer request/response identities', () => {
    it('shows the agent on requests and the LLM provider on responses', () => {
        render(
            <RunLogViewer
                autoScroll={false}
                agentName="Orchestrator"
                messages={[
                    message(1, {
                        type: 'request',
                        agent_name: 'Orchestrator',
                        content: JSON.stringify({ messages: [{ role: 'user', content: 'Task context' }] }),
                    }),
                    message(2, {
                        type: 'response',
                        content: JSON.stringify({ content: 'I will inspect the task.' }),
                    }),
                    message(3, {
                        type: 'request',
                        agent_name: 'CEO Agent',
                        content: JSON.stringify({ messages: [{ role: 'user', content: 'Continue with the next step.' }] }),
                    }),
                ]}
            />,
        );

        expect(screen.getAllByText('LLM Provider')).toHaveLength(1);
        expect(screen.getAllByText('Orchestrator')).toHaveLength(2);
        expect(screen.queryByText('CEO Agent')).toBeNull();
        expect(screen.queryByText('AI Model')).toBeNull();
    });
});
