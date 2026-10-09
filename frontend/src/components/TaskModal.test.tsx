import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import axios from 'axios';
import { TaskModal } from './TaskModal';
import { useStore } from '../store';

vi.mock('axios', () => ({ default: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }));
vi.mock('../useWebSocket', () => ({ useWebSocket: vi.fn(), wsUrl: () => 'ws://test' }));

const baseTask = {
    id: 5, company_id: 3, root_task_id: 5, depth: 0, title: 'Write the report', description: 'About boats', ref_key: 'ACME-5',
    status: 'blocked', task_type: 'research', mode: 'managed', phase: 'refine', waiting_on: 'human', wait_detail: 'needs your answer',
    priority: 'Normal', sprint_id: 1, is_archived: false, git_base_branch: 'main',
};

// serve answers every GET the modal makes for the given task and its comments.
function serve(task: Record<string, unknown>, comments: unknown[]) {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
        if (url === '/api/tasks/5') return { data: task } as never;
        if (url === '/api/comments?task_id=5') return { data: comments } as never;
        if (url === '/api/tasks/5/tree') return { data: [task] } as never;
        if (url === '/api/tasks/5/usage') return { data: { totals: { calls: 0, failed_calls: 0, prompt_tokens: 0, completion_tokens: 0, reasoning_tokens: 0, cached_tokens: 0, duration_ms: 0 }, groups: {} } } as never;
        return { data: [] } as never;
    });
}

const renderModal = () => render(
    <MemoryRouter initialEntries={['/companies/acme/tasks/5']}>
        <Routes>
            <Route path="/companies/:shortName/tasks/:taskId" element={<TaskModal taskId={5} onClose={() => {}} standalone />} />
        </Routes>
    </MemoryRouter>,
);

describe('TaskModal in the workflow', () => {
    afterEach(cleanup);
    beforeEach(() => {
        vi.clearAllMocks();
        useStore.setState({ user: null, companies: [], selectedCompanyId: 3 });
        vi.mocked(axios.post).mockResolvedValue({ data: { id: 99 } } as never);
        vi.mocked(axios.put).mockResolvedValue({ data: {} } as never);
    });

    it('shows where the task is and answers each open question on its own', async () => {
        serve(baseTask, [
            { id: 11, task_id: 5, author_type: 'agent', comment_type: 'ask_user', content: 'Which region?', created_at: '2026-10-08T10:00:00Z' },
            { id: 12, task_id: 5, author_type: 'agent', comment_type: 'ask_user', content: 'Which year?', created_at: '2026-10-08T10:01:00Z' },
        ]);
        renderModal();

        const state = await screen.findByTestId('task-state');
        expect(state.textContent).toContain('Research');
        expect(state.textContent).toContain('Blocked');
        expect(within(state).getByTestId('phase-chip').textContent).toContain('waiting for your answer');

        const dialogues = await screen.findAllByTestId('question-dialogue');
        expect(dialogues).toHaveLength(2);
        // Answer the second question, not the oldest.
        const second = within(dialogues[1]);
        fireEvent.change(second.getByLabelText('Your answer'), { target: { value: '2025' } });
        fireEvent.click(second.getByRole('button', { name: 'Answer' }));

        await waitFor(() => expect(axios.post).toHaveBeenCalledWith('/api/comments', {
            task_id: 5, author_type: 'human', content: '2025', reply_to_id: 12,
        }));
    });

    it('offers stop while the task works and run again once it is at rest', async () => {
        serve({ ...baseTask, status: 'in-progress', phase: 'execute', waiting_on: 'subtasks', wait_detail: '' }, []);
        const first = renderModal();
        fireEvent.click(await screen.findByTestId('task-stop'));
        await waitFor(() => expect(axios.post).toHaveBeenCalledWith('/api/tasks/5/stop'));
        expect(screen.queryByTestId('task-rerun')).toBeNull();
        // While it runs the status cannot be changed by hand.
        expect((screen.getByLabelText('Status') as HTMLSelectElement).disabled).toBe(true);
        first.unmount();

        serve({ ...baseTask, status: 'in-review', phase: '', waiting_on: '', wait_detail: '', result_summary: 'Boats float.' }, []);
        renderModal();
        fireEvent.click(await screen.findByTestId('task-rerun'));
        await waitFor(() => expect(axios.post).toHaveBeenCalledWith('/api/tasks/5/rerun'));
        expect(screen.queryByTestId('task-stop')).toBeNull();
        expect(screen.getByTestId('task-result').textContent).toContain('Boats float.');
    });

    it('saves the task type and its own model, and clears the model when none is chosen', async () => {
        const provider = { id: 2, name: 'Strong', default_model: 'strong-1', supported_models: 'strong-1,strong-2' };
        serve({ ...baseTask, status: 'backlog', phase: '', waiting_on: '', wait_detail: '', provider_id: 2, model: 'strong-1' }, []);
        vi.mocked(axios.get).mockImplementation(async (url: string) => {
            if (url === '/api/tasks/5') return { data: { ...baseTask, status: 'backlog', phase: '', waiting_on: '', wait_detail: '', provider_id: 2, model: 'strong-1' } } as never;
            if (url === '/api/providers') return { data: [provider] } as never;
            return { data: [] } as never;
        });
        renderModal();

        fireEvent.change(await screen.findByLabelText('Type'), { target: { value: 'coding' } });
        const model = within(screen.getByTestId('task-model'));
        await waitFor(() => expect(model.getByRole('option', { name: 'Strong' })).toBeTruthy());
        // Back to the default of the task's tier.
        fireEvent.change(model.getAllByRole('combobox')[0], { target: { value: '' } });
        fireEvent.click(screen.getByRole('button', { name: /Save Task/ }));

        await waitFor(() => expect(axios.put).toHaveBeenCalled());
        const [url, payload] = vi.mocked(axios.put).mock.calls[0] as [string, Record<string, unknown>];
        expect(url).toBe('/api/tasks/5');
        expect(payload.task_type).toBe('coding');
        expect(payload.provider_id).toBeNull();
        expect(payload.model_group_id).toBeNull();
        expect(payload.model).toBe('');
        expect(payload).not.toHaveProperty('status');
    });
});
