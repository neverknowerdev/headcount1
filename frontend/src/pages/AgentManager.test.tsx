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
    description: 'Implements code.',
    prompt: 'Implement the approved specification.',
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
});
