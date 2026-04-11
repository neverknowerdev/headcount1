import React from 'react';
import { Link } from 'react-router-dom';
import { Home, Users, Activity } from 'lucide-react';

export const Layout: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  return (
    <div className="flex h-screen bg-gray-100">
      <aside className="w-64 bg-white border-r flex flex-col">
        <div className="p-4 border-b">
          <h1 className="text-xl font-bold text-indigo-600">Forge Orchestrator</h1>
        </div>
        <nav className="flex-1 p-4 space-y-2">
          <Link to="/" className="flex items-center space-x-2 text-gray-700 p-2 rounded hover:bg-gray-50">
            <Home size={20} />
            <span>Dashboard</span>
          </Link>
          <Link to="/agents" className="flex items-center space-x-2 text-gray-700 p-2 rounded hover:bg-gray-50">
            <Users size={20} />
            <span>Agents</span>
          </Link>
          <Link to="/runs" className="flex items-center space-x-2 text-gray-700 p-2 rounded hover:bg-gray-50">
            <Activity size={20} />
            <span>Run Logs</span>
          </Link>
        </nav>
      </aside>
      <main className="flex-1 overflow-auto">
        <div className="p-8">
          {children}
        </div>
      </main>
    </div>
  );
};
