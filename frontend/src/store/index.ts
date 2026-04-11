import { create } from 'zustand';

interface Company {
    id: number;
    name: string;
    short_name: string;
    color: string;
}

interface AppState {
    companies: Company[];
    selectedCompanyId: number | null;
    setCompanies: (companies: Company[]) => void;
    setSelectedCompanyId: (id: number | null) => void;

    isFirstOpen: boolean;
    setIsFirstOpen: (isFirstOpen: boolean) => void;
}

export const useStore = create<AppState>((set) => ({
    companies: [],
    selectedCompanyId: null,
    setCompanies: (companies) => set({ companies }),
    setSelectedCompanyId: (selectedCompanyId) => set({ selectedCompanyId }),

    isFirstOpen: false,
    setIsFirstOpen: (isFirstOpen) => set({ isFirstOpen }),
}));
