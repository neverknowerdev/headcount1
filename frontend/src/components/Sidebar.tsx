import React, { useMemo } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { LayoutDashboard, CheckSquare, FolderOpen, Users, Code, Activity, Settings } from 'lucide-react';
import { useStore } from '../store';

const getNavItems = (companyId: number | null) => {
  const base = companyId ? `/companies/${companyId}` : '';
  return [
    { icon: LayoutDashboard, label: 'Dashboard', path: base ? base : '/' },
    { icon: CheckSquare, label: 'Tasks', path: `${base}/tasks` },
    { icon: FolderOpen, label: 'Projects', path: `${base}/projects` },
    { icon: Users, label: 'Agents', path: `${base}/agents` },
    { icon: Settings, label: 'LLM Providers', path: `${base}/providers` },
    { icon: Code, label: 'Skills', path: `${base}/skills` },
    { icon: Activity, label: 'Run Logs', path: `${base}/runs` },
    { icon: Settings, label: 'Settings', path: `${base}/settings` },
  ];
};

export const Sidebar: React.FC = () => {
  const location = useLocation();
  const { selectedCompanyId, companies } = useStore();
  const navItems = useMemo(() => getNavItems(selectedCompanyId), [selectedCompanyId]);

  const currentCompany = companies.find((c) => c.id === selectedCompanyId);

  const handleShortNameUpdate = async (e: React.FocusEvent<HTMLInputElement>) => {
      const newShortName = e.target.value;
      if (!currentCompany || newShortName === currentCompany.short_name) return;

      try {
         await fetch(`/api/companies/${currentCompany.id}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ short_name: newShortName })
         });
         // We might want to update the store here, but since the user just requested DB save we do our best.
      } catch (err) {
         console.error("Failed to update short name", err);
      }
  };


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

      {currentCompany && (
        <div className="p-4 border-t border-gray-200">
           <label className="block text-xs font-medium text-gray-500 uppercase tracking-wider mb-2">Short Name (Slack)</label>
           <input
              type="text"
              maxLength={2}
              defaultValue={currentCompany.short_name.substring(0,2)}
              onBlur={handleShortNameUpdate}
              className="w-full text-sm font-mono bg-gray-50 border border-gray-300 rounded px-2 py-1 text-center uppercase focus:ring-indigo-500 focus:border-indigo-500 focus:outline-none"
              title="Displayed in left Slack company menu. Max 2 chars."
           />
        </div>
      )}
    </div>
  );
};
