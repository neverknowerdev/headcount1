import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { CompanySwitcher } from './CompanySwitcher';
import { Sidebar } from './Sidebar';
import { MemoryModelAlert } from './MemoryModelAlert';
import { MemoryBackendAlert } from './MemoryBackendAlert';
import { useStore } from '../store';
import { AddCompany } from '../pages/AddCompany';
import { useLocation, useNavigate } from 'react-router-dom';

interface LayoutProps {
  children: React.ReactNode;
}

// Inner component to access router hooks inside BrowserRouter
const LayoutContent: React.FC<LayoutProps> = ({ children }) => {
  const { setCompanies, setSelectedCompanyId, companies, selectedCompanyId } = useStore();
  const [loading, setLoading] = useState(true);
  const location = useLocation();
  const navigate = useNavigate();

  useEffect(() => {
    const fetchInitialData = async () => {
      try {
        const res = await axios.get('/api/companies');
        const comps = res.data || [];
        setCompanies(comps);

        if (comps.length > 0) {
            // Check if URL has companies/:id
            const match = location.pathname.match(/\/companies\/([^\/]+)/);
            if (match) {
                const urlShortName = match[1];
                const compExists = comps.find((c: any) => c.short_name === urlShortName);
                if (compExists) {
                     setSelectedCompanyId(compExists.id);
                } else {
                     // Invalid company short_name in URL, fallback
                     setSelectedCompanyId(comps[0].id);
                     navigate(`/companies/${comps[0].short_name}`);
                }
            } else if (!selectedCompanyId) {
                setSelectedCompanyId(comps[0].id);
                if (location.pathname === '/') {
                   navigate(`/companies/${comps[0].short_name}`);
                }
            }
        }
      } catch (e) {
        console.error(e);
      } finally {
        setLoading(false);
      }
    };

    fetchInitialData();
  }, [setCompanies, setSelectedCompanyId, companies.length, location.pathname, navigate, selectedCompanyId]); 

  // If there are no companies at all, force redirect to /add-company
  useEffect(() => {
    // The Team page works without any company (e.g. a freshly invited
    // member before the team has created one).
    if (!loading && companies.length === 0 && location.pathname !== '/add-company' && location.pathname !== '/team') {
      window.location.href = '/add-company';
    }
  }, [loading, companies.length, location.pathname]);

  if (loading) {
    return <div className="h-screen w-screen flex items-center justify-center bg-gray-50">Loading...</div>;
  }

  if (location.pathname === '/add-company') {
      return <AddCompany />;
  }

  if (loading || (companies.length === 0 && location.pathname !== '/add-company' && location.pathname !== '/team')) {
    return <div className="h-screen w-screen flex items-center justify-center bg-gray-50">Loading...</div>;
  }

  return (
    <div className="h-screen flex overflow-hidden bg-gray-50">
      <CompanySwitcher />
      <Sidebar />
      <main className="flex-1 overflow-y-auto p-8">
        <MemoryBackendAlert />
        <MemoryModelAlert />
        {children}
      </main>
    </div>
  );
};

export const Layout: React.FC<LayoutProps> = ({ children }) => {
    return <LayoutContent>{children}</LayoutContent>;
}
