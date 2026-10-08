import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
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

const coder = {
    id: 8,
    name: 'Coder',
    role_key: 'Coder',
    short_name: 'CODER',
    builtin: true,
    enabled: true,
    description: 'Implements code.',
    system_prompt: 'You are the Coder agent.',
};

describe('AgentManager', () => {
    afterEach(cleanup);

    beforeEach(() => {
        vi.clearAllMocks();
        useStore.setState({
            user: null,
            companies: [],
            selectedCompanyId: 42,
        });
        vi.mocked(axios.get).mockResolvedValue({ data: [] } as never);
        vi.mocked(axios.post).mockResolvedValue({ data: { id: 7 } } as never);
    });

    it('creates a custom agent as a role with a one-line prompt', async () => {
        render(
            <MemoryRouter initialEntries={['/companies/acme/agents']}>
                <AgentManager />
            </MemoryRouter>,
        );

        fireEvent.click(await screen.findByRole('button', { name: '+ Add agent' }));
        // An agent is a role: no template, model, tool or MCP choices.
        expect(screen.queryByTestId('agent-template')).toBeNull();
        fireEvent.change(screen.getByLabelText(/Name/), { target: { value: 'Boat expert' } });
        fireEvent.click(screen.getByRole('button', { name: 'Create agent' }));

        await waitFor(() => expect(axios.post).toHaveBeenCalledWith('/api/agents', {
            company_id: 42,
            name: 'Boat expert',
            description: '',
            system_prompt: 'You are the Boat expert agent.',
        }));
    });

    it('keeps the prompt a person wrote for a custom agent', async () => {
        render(
            <MemoryRouter initialEntries={['/companies/acme/agents']}>
                <AgentManager />
            </MemoryRouter>,
        );

        fireEvent.click(await screen.findByRole('button', { name: '+ Add agent' }));
        fireEvent.change(screen.getByLabelText(/Name/), { target: { value: 'Boat expert' } });
        fireEvent.change(screen.getByLabelText('Prompt'), { target: { value: 'You know everything about boats.' } });
        fireEvent.click(screen.getByRole('button', { name: 'Create agent' }));

        await waitFor(() => expect(axios.post).toHaveBeenCalledWith('/api/agents', expect.objectContaining({
            system_prompt: 'You know everything about boats.',
        })));
    });

    it('shows built-in roles compactly and expands to their prompt, without enable controls or tool lists', async () => {
        vi.mocked(axios.get).mockImplementation(async (url: string) => {
            if (url === '/api/agents?company_id=42') return { data: [coder] } as never;
            return { data: [] } as never;
        });

        render(
            <MemoryRouter initialEntries={['/companies/acme/agents']}>
                <AgentManager />
            </MemoryRouter>,
        );

        expect(await screen.findByTestId('builtin-agent-8')).toBeTruthy();
        expect(screen.queryByText('You are the Coder agent.')).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: 'Expand Coder' }));

        expect(screen.getByText('You are the Coder agent.')).toBeTruthy();
        expect(screen.queryByText('Available tools')).toBeNull();
        expect(screen.getByText('Always enabled')).toBeTruthy();
        expect(screen.queryByRole('switch', { name: /Coder/ })).toBeNull();
        expect(screen.getByRole('button', { name: 'Edit prompt, see usage →' })).toBeTruthy();
    });

    it('deletes a custom agent but does not render a delete action for built-ins', async () => {
        vi.mocked(axios.get).mockImplementation(async (url: string) => {
            if (url === '/api/agents?company_id=42') return { data: [
                coder,
                { id: 9, name: 'Research helper', builtin: false, enabled: true, system_prompt: 'Research carefully.' },
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
