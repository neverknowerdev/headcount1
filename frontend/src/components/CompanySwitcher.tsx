/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState } from 'react';
import axios from 'axios';
import { useStore, useIsOwner } from '../store';
import { Plus, LogOut, Settings as SettingsIcon, Users, User } from 'lucide-react';
import { useNavigate, useLocation } from 'react-router-dom';

export const CompanySwitcher: React.FC = () => {
    const { companies, selectedCompanyId, setSelectedCompanyId, user, setUser } = useStore();
    const isOwner = useIsOwner();
    const navigate = useNavigate();
    const location = useLocation();
    const [menuOpen, setMenuOpen] = useState(false);

    const getInitials = (company: any) => {
        if (company.short_name) {
             return company.short_name.substring(0,2).toUpperCase();
        }
        return company.name.split(' ').map((n: string) => n[0]).join('').substring(0, 2).toUpperCase();
    };

    const logout = async () => {
        try {
            await axios.post('/api/auth/logout');
        } finally {
            setUser(null); // AuthGate flips back to the login screen
        }
    };

    const go = (path: string) => {
        setMenuOpen(false);
        navigate(path);
    };

    return (
        <div className="w-12 shrink-0 bg-gray-900 flex flex-col items-center py-3 h-full sm:w-16 sm:py-4">
            <div className="flex flex-col items-center space-y-3 sm:space-y-4">
                {companies.map(company => (
                    <button
                        key={company.id}
                        aria-label={company.name}
                        onClick={() => {
                            setSelectedCompanyId(company.id);
                            navigate(`/companies/${company.short_name}`);
                        }}
                        className={`grid size-9 place-items-center rounded-full text-xs font-bold text-white transition-transform hover:scale-105 sm:size-12 sm:text-base ${selectedCompanyId === company.id ? 'ring-4 ring-white ring-opacity-50' : ''}`}
                        style={{ backgroundColor: company.color || '#4f46e5' }}
                        title={company.name}
                    >
                        {getInitials(company)}
                    </button>
                ))}

                {isOwner && (
                    <>
                        <div className="w-8 border-t border-gray-700 my-2"></div>
                        <button
                            onClick={() => navigate('/add-company')}
                            aria-label="Add Workspace"
                            className="grid size-9 place-items-center rounded-full bg-gray-800 text-gray-400 transition-colors hover:bg-gray-700 hover:text-white sm:size-12"
                            title="Add Workspace"
                        >
                            <Plus size={24} />
                        </button>
                    </>
                )}
            </div>

            {/* User menu pinned to the bottom of the rail. */}
            {user && (
                <div className="relative mt-auto">
                    <button
                        onClick={() => setMenuOpen(o => !o)}
                        aria-label={`Account menu for ${user.email}`}
                        className={`grid size-9 place-items-center rounded-full bg-purple-600 text-white font-bold transition-transform hover:scale-105 sm:size-12 ${menuOpen ? 'ring-4 ring-white ring-opacity-50' : ''}`}
                        title={user.email}
                    >
                        <User size={22} />
                    </button>

                    {menuOpen && (
                        <>
                            {/* Click-away backdrop. */}
                            <div className="fixed inset-0 z-40" onClick={() => setMenuOpen(false)} />
                            <div className="absolute bottom-0 left-14 z-50 w-60 rounded-lg border border-gray-200 bg-white shadow-xl py-1">
                                <div className="px-4 py-3 border-b">
                                    <p className="text-xs text-gray-400">Signed in as</p>
                                    <p className="truncate text-sm font-medium text-gray-900" title={user.email}>{user.email}</p>
                                    {user.role && (
                                        <span className="mt-1 inline-block rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-600 capitalize">{user.role}</span>
                                    )}
                                </div>
                                <button
                                    onClick={() => go('/team')}
                                    className={`flex w-full items-center gap-2 px-4 py-2 text-sm hover:bg-gray-50 ${location.pathname === '/team' ? 'text-indigo-600' : 'text-gray-700'}`}
                                >
                                    <Users size={16} /> Team
                                </button>
                                <button
                                    onClick={() => go(selectedCompanyId ? `/companies/${companies.find(c => c.id === selectedCompanyId)?.short_name}/settings` : '/settings')}
                                    className="flex w-full items-center gap-2 px-4 py-2 text-sm text-gray-700 hover:bg-gray-50"
                                >
                                    <SettingsIcon size={16} /> Settings
                                </button>
                                <div className="my-1 border-t" />
                                <button
                                    onClick={logout}
                                    className="flex w-full items-center gap-2 px-4 py-2 text-sm text-red-600 hover:bg-red-50"
                                >
                                    <LogOut size={16} /> Log out
                                </button>
                            </div>
                        </>
                    )}
                </div>
            )}
        </div>
    );
};
