import React, { useMemo, useEffect, useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { LayoutDashboard, CheckSquare, FolderOpen, Users, Code, Activity, Settings, Cpu } from 'lucide-react';
import { useStore } from '../store';
import axios from 'axios';
import { Attribution } from './Attribution';

const getNavItems = (companyIdentifier: string | null) => {
  const base = companyIdentifier ? `/companies/${companyIdentifier}` : '';
  return [
    { icon: LayoutDashboard, label: 'Dashboard', path: base ? base : '/' },
    { icon: CheckSquare, label: 'Tasks', path: `${base}/tasks` },
    { icon: FolderOpen, label: 'Projects', path: `${base}/projects` },
    { icon: Users, label: 'Agents', path: `${base}/agents` },
    { icon: Code, label: 'Skills', path: `${base}/skills` },
    { icon: Cpu, label: 'MCP Servers', path: `${base}/mcp-servers` },
    { icon: Settings, label: 'LLM Providers', path: `${base}/providers` },
    { icon: Activity, label: 'Run Logs', path: `${base}/runs` },
    { icon: Settings, label: 'Settings', path: `${base}/settings` },
  ];
};

export const Sidebar: React.FC = () => {
  const location = useLocation();
  const { selectedCompanyId, companies } = useStore();

  const currentCompany = companies.find((c) => c.id === selectedCompanyId);
  const navItems = useMemo(() => getNavItems(currentCompany ? currentCompany.short_name : null), [currentCompany]);

  // version is the human-facing number (2026.07.29 in production,
  // staging-<short branch>-<short commit> on staging); build is the exact
  // build identity, shown on hover for support and bug reports.
  const [version, setVersion] = useState<string>('');
  const [build, setBuild] = useState<string>('');

  useEffect(() => {
    axios.get('/api/version').then(res => {
      setVersion(res.data.version || 'dev');
      setBuild(res.data.display || '');
    }).catch(() => {});
  }, []);





  return (
    <div className="w-12 shrink-0 bg-white border-r flex flex-col h-full sm:w-64">
      <div className="flex-1 overflow-y-auto py-4">
        <nav className="space-y-1 px-2">
          {navItems.map((item) => {
            let isActive = false;

            // Strict match for dashboard, prefix match for others to keep active state on subpages
            if (item.label === 'Dashboard') {
               isActive = location.pathname === item.path || (location.pathname === '/' && item.path.endsWith('/'));
            } else {
               isActive = location.pathname.startsWith(item.path);
            }

            return (
              <Link
                key={item.label}
                to={item.path}
                aria-label={item.label}
                title={item.label}
                className={`${
                  isActive
                    ? 'bg-indigo-50 text-indigo-600'
                    : 'text-gray-600 hover:bg-gray-50 hover:text-gray-900'
                  } group flex items-center justify-center px-2 py-2 text-sm font-medium rounded-md sm:justify-start`}
              >
                <item.icon
                  className={`${
                    isActive ? 'text-indigo-600' : 'text-gray-400 group-hover:text-gray-500'
                  } h-5 w-5 flex-shrink-0 sm:mr-3`}
                  aria-hidden="true"
                />
                <span className="sr-only sm:not-sr-only">{item.label}</span>
              </Link>
            );
          })}
        </nav>
      </div>

      <div className="px-3 py-3 border-t border-gray-100">
        {version && (
          <div
            className="text-xs text-gray-400 font-mono truncate"
            title={build ? `${version}\n${build}` : version}
          >
            {version}
          </div>
        )}
        <div className="hidden sm:block mt-1">
          <Attribution />
        </div>
      </div>
    </div>
  );
};
