import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import axios from 'axios';
import { AgentManager } from './AgentManager';
import { useStore } from '../store';

vi.mock('axios', () => ({
    default: {
        get: vi.fn(),
        post: vi.fn(),
        put: vi.fn(),
        delete: vi.fn(),
    },
}));

const coderTemplate = {
    name: 'Coder',
    canonical_name: 'Coder',
    slug: 'CODER',
    description: 'Implements code.',
    prompt: 'Implement the approved specification.',
    best_models: ['openai/gpt-5-codex', 'anthropic/claude-sonnet-4'],
    allowed_tools: ['read', 'write'],
    permissions: '{"browser_use":"deny"}',
};

describe('AgentManager templates', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        useStore.setState({
            user: null,
            companies: [],
            selectedCompanyId: 42,
        });
        vi.mocked(axios.get).mockImplementation(async (url: string) => {
            if (url === '/api/agent-configs') return { data: [coderTemplate] } as never;
            return { data: [] } as never;
        });
        vi.mocked(axios.post).mockResolvedValue({ data: { id: 7 } } as never);
    });

    it('copies a selected template prompt and tool settings into the create form', async () => {
        render(
            <MemoryRouter initialEntries={['/companies/acme/agents']}>
                <AgentManager />
            </MemoryRouter>,
        );

        fireEvent.click(await screen.findByRole('button', { name: '+ Add agent' }));
        const templateSelect = await screen.findByTestId('agent-template');
        fireEvent.change(templateSelect, { target: { value: 'Coder' } });

        expect((screen.getByLabelText('System prompt') as HTMLTextAreaElement).value).toBe(coderTemplate.prompt);
        expect(screen.getByText('Copied the template prompt and 2 tool settings. You can edit the prompt below.')).toBeTruthy();

        fireEvent.change(screen.getByLabelText(/Name/), { target: { value: 'Implementation assistant' } });
        fireEvent.click(screen.getByRole('button', { name: 'Create agent' }));

        await waitFor(() => expect(axios.post).toHaveBeenCalledWith('/api/agents', {
            company_id: 42,
            name: 'Implementation assistant',
            description: '',
            system_prompt: coderTemplate.prompt,
            permissions: coderTemplate.permissions,
        }));
    });

    it('shows built-in agents compactly and expands their identity, tools, and model recommendations', async () => {
        vi.mocked(axios.get).mockImplementation(async (url: string) => {
            if (url === '/api/agent-configs') return { data: [coderTemplate] } as never;
            if (url === '/api/agents?company_id=42') return { data: [{
                id: 8,
                name: 'Coder',
                role_key: 'Coder',
                short_name: 'CODER',
                builtin: true,
                enabled: true,
                model: 'openrouter/free',
                description: 'Implements code.',
                system_prompt: 'Implement the approved specification.',
            }] } as never;
            return { data: [] } as never;
        });

        render(
            <MemoryRouter initialEntries={['/companies/acme/agents']}>
                <AgentManager />
            </MemoryRouter>,
        );

        expect(await screen.findByTestId('builtin-agent-8')).toBeTruthy();
        expect(screen.queryByText('Implement the approved specification.')).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: 'Expand Coder' }));

        expect(screen.getByText('Canonical system name')).toBeTruthy();
        expect(screen.getByText('CODER')).toBeTruthy();
        expect(screen.getByText('read')).toBeTruthy();
        expect(screen.getByText('write')).toBeTruthy();
        expect(screen.queryByText('openai/gpt-5-codex')).toBeNull();
        expect(screen.queryByText('Built-in', { exact: true })).toBeNull();
        expect(screen.queryByText('openrouter/free', { exact: true })).toBeNull();
        expect(screen.getByRole('switch', { name: 'Disable Coder' })).toBeTruthy();
        expect(screen.getByRole('button', { name: 'Open edit page →' })).toBeTruthy();
    });

    it('deletes a custom agent but does not render a delete action for built-ins', async () => {
        vi.mocked(axios.get).mockImplementation(async (url: string) => {
            if (url === '/api/agent-configs') return { data: [coderTemplate] } as never;
            if (url === '/api/agents?company_id=42') return { data: [
                {
                    id: 8, name: 'Coder', role_key: 'Coder', short_name: 'CODER', builtin: true,
                    enabled: true, model: 'openrouter/free', description: 'Implements code.',
                    system_prompt: 'Implement the approved specification.',
                },
                {
                    id: 9, name: 'Research helper', builtin: false, enabled: true,
                    model: 'custom-model', system_prompt: 'Research carefully.',
                },
            ] } as never;
            return { data: [] } as never;
        });
        vi.mocked(axios.delete).mockResolvedValue({ data: { message: 'agent deleted' } } as never);
        vi.stubGlobal('confirm', vi.fn(() => true));

        render(
            <MemoryRouter initialEntries={['/companies/acme/agents']}>
                <AgentManager />
            </MemoryRouter>,
        );

        expect(await screen.findByRole('switch', { name: 'Disable Research helper' })).toBeTruthy();
        fireEvent.click(await screen.findByRole('button', { name: 'Delete Research helper' }));
        await waitFor(() => expect(axios.delete).toHaveBeenCalledWith('/api/agents/9'));
        expect(screen.queryByRole('button', { name: 'Delete Coder' })).toBeNull();
    });
});
