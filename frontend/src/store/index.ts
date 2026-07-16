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
