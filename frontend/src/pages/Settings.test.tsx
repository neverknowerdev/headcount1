import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import axios from 'axios';
import { Settings } from './Settings';
import { useStore } from '../store';

vi.mock('axios', () => ({
    default: {
        get: vi.fn(),
        post: vi.fn(),
        put: vi.fn(),
        delete: vi.fn(),
    },
}));

function renderSettings(isAdmin: boolean) {
    useStore.setState({
        user: { id: isAdmin ? 1 : 2, email: `${isAdmin ? 'admin' : 'member'}@test.local`, is_admin: isAdmin },
        companies: [],
        selectedCompanyId: null,
    });
    return render(
        <MemoryRouter initialEntries={['/companies/ac/settings']}>
            <Settings />
        </MemoryRouter>,
    );
}

describe('Settings deployment panel', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        vi.mocked(axios.get).mockImplementation(async (url: string) => {
            if (url === '/api/settings') {
                return { data: { deploy_source: 'main', auto_deploy: true } } as never;
            }
            return {
                data: {
                    environment: 'production',
                    deploy_source: 'main',
                    auto_deploy: true,
                },
            } as never;
        });
        vi.mocked(axios.post).mockResolvedValue({ data: {} } as never);
        vi.stubGlobal('alert', vi.fn());
    });

    afterEach(() => {
        useStore.setState({ user: null, companies: [], selectedCompanyId: null });
        vi.restoreAllMocks();
    });

    it('shows the deployment Save button to the admin and persists its values', async () => {
        renderSettings(true);

        await screen.findByRole('heading', { name: 'Deployment' });
        fireEvent.click(screen.getByRole('button', { name: 'Save Deployment Settings' }));

        await waitFor(() => expect(axios.post).toHaveBeenCalledWith('/api/settings', {
            deploy_source: 'main',
            auto_deploy: true,
        }));
        expect(globalThis.alert).toHaveBeenCalledWith('Deployment settings saved!');
    });

    it('does not render deployment settings on staging', async () => {
        vi.mocked(axios.get).mockImplementation(async (url: string) => {
            if (url === '/api/settings') return { data: {} } as never;
            return { data: { environment: 'staging', deploy_source: 'main', auto_deploy: true } } as never;
        });

        renderSettings(true);
        await waitFor(() => expect(axios.get).toHaveBeenCalledWith('/api/deploy/status'));
        expect(screen.queryByRole('heading', { name: 'Deployment' })).toBeNull();
    });

    it('does not request or render deployment settings for a non-admin', async () => {
        renderSettings(false);
        await waitFor(() => expect(axios.get).toHaveBeenCalledWith('/api/settings'));
        expect(axios.get).not.toHaveBeenCalledWith('/api/deploy/status');
        expect(screen.queryByRole('heading', { name: 'Deployment' })).toBeNull();
    });
});
