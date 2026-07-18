import { create } from 'zustand';

interface Company {
    id: number;
    name: string;
    short_name: string;
    color: string;
}

export interface AuthUser {
    id: number;
    email: string;
    // locked = authenticated but the encryption vault is not unlocked (e.g.
    // after a server crash); the UI prompts for a passkey re-tap.
    locked?: boolean;
    // session_expires_at = when this login hits its hard ceiling (RFC3339). At
    // that point refresh fails and the keyring lapses, so agents stop; the UI
    // gates on it before it happens. reauth_at = when to start nudging a
    // passkey re-auth (ceiling − safety gap). Absent for cookieless/E2E.
    session_expires_at?: string;
    reauth_at?: string;
    reauth_required?: boolean;
}

interface AppState {
    companies: Company[];
    selectedCompanyId: number | null;
    setCompanies: (companies: Company[]) => void;
    setSelectedCompanyId: (id: number | null) => void;

    isFirstOpen: boolean;
    setIsFirstOpen: (isFirstOpen: boolean) => void;

    // Authenticated user (null = logged out). AuthGate sets it after probing
    // /api/auth/me; the global 401 interceptor clears it.
    user: AuthUser | null;
    setUser: (user: AuthUser | null) => void;
}

export const useStore = create<AppState>((set) => ({
    companies: [],
    selectedCompanyId: null,
    setCompanies: (companies) => set({ companies }),
    setSelectedCompanyId: (selectedCompanyId) => set({ selectedCompanyId }),

    isFirstOpen: false,
    setIsFirstOpen: (isFirstOpen) => set({ isFirstOpen }),

    user: null,
    setUser: (user) => set({ user }),
}));
