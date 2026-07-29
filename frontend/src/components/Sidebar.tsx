import React, { useMemo } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { LayoutDashboard, CheckSquare, FolderOpen, Users, Code, Activity, Settings, Cpu, KeyRound } from 'lucide-react';
import { useStore } from '../store';

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
    { icon: KeyRound, label: 'Environments', path: `${base}/environments` },
    { icon: Activity, label: 'Run Logs', path: `${base}/runs` },
    { icon: Settings, label: 'Settings', path: `${base}/settings` },
  ];
};

export const Sidebar: React.FC = () => {
  const location = useLocation();
  const { selectedCompanyId, companies } = useStore();

  const currentCompany = companies.find((c) => c.id === selectedCompanyId);
  const navItems = useMemo(() => getNavItems(currentCompany ? currentCompany.short_name : null), [currentCompany]);





  return (
    <div className="w-64 bg-white border-r flex flex-col h-full">
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
                className={`${
                  isActive
                    ? 'bg-indigo-50 text-indigo-600'
                    : 'text-gray-600 hover:bg-gray-50 hover:text-gray-900'
                } group flex items-center px-2 py-2 text-sm font-medium rounded-md`}
              >
                <item.icon
                  className={`${
                    isActive ? 'text-indigo-600' : 'text-gray-400 group-hover:text-gray-500'
                  } mr-3 flex-shrink-0 h-5 w-5`}
                  aria-hidden="true"
                />
                {item.label}
              </Link>
            );
          })}
        </nav>
      </div>

    </div>
  );
};
