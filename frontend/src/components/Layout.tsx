import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { CompanySwitcher } from './CompanySwitcher';
import { Sidebar } from './Sidebar';
import { useStore } from '../store';
import { AddCompany } from '../pages/AddCompany';
import { useLocation } from 'react-router-dom';

interface LayoutProps {
  children: React.ReactNode;
}

export const Layout: React.FC<LayoutProps> = ({ children }) => {
  const { setCompanies, setSelectedCompanyId, companies, selectedCompanyId } = useStore();
  const [loading, setLoading] = useState(true);
  const location = useLocation();

  useEffect(() => {
    const fetchInitialData = async () => {
      try {
        const res = await axios.get('/api/companies');
        const comps = res.data || [];
        setCompanies(comps);

        if (comps.length > 0 && !selectedCompanyId) {
          setSelectedCompanyId(comps[0].id);
        }
      } catch (e) {
        console.error(e);
      } finally {
        setLoading(false);
      }
    };

    fetchInitialData();
  }, [setCompanies, setSelectedCompanyId, selectedCompanyId]);

  if (loading) {
    return <div className="h-screen w-screen flex items-center justify-center bg-gray-50">Loading...</div>;
  }

  // If there are no companies at all, force redirect to /add-company
  if (companies.length === 0 && location.pathname !== '/add-company') {
      window.location.href = '/add-company';
      return null;
  }

  // Allow /add-company to render full screen, regardless of how many companies exist.
  if (location.pathname === '/add-company') {
      return <AddCompany />;
  }


  return (
    <div className="h-screen flex overflow-hidden bg-gray-50">
      <CompanySwitcher />
      <Sidebar />
      <main className="flex-1 overflow-y-auto p-8">
        {children}
      </main>
    </div>
  );
};
