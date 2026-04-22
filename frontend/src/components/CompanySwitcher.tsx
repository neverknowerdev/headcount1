/* eslint-disable @typescript-eslint/no-explicit-any */
import React from 'react';
import { useStore } from '../store';
import { Plus } from 'lucide-react';
import { useNavigate } from 'react-router-dom';

export const CompanySwitcher: React.FC = () => {
    const { companies, selectedCompanyId, setSelectedCompanyId } = useStore();
    const navigate = useNavigate();

    const getInitials = (company: any) => {
        if (company.short_name) {
             return company.short_name.substring(0,2).toUpperCase();
        }
        return company.name.split(' ').map((n: string) => n[0]).join('').substring(0, 2).toUpperCase();
    };

    return (
        <div className="w-16 bg-gray-900 flex flex-col items-center py-4 space-y-4 h-full">
            {companies.map(company => (
                <button
                    key={company.id}
                    onClick={() => {
                        setSelectedCompanyId(company.id);
                        navigate(`/companies/${company.short_name}`);
                    }}
                    className={`w-12 h-12 rounded-full flex items-center justify-center text-white font-bold transition-transform hover:scale-105 ${selectedCompanyId === company.id ? 'ring-4 ring-white ring-opacity-50' : ''}`}
                    style={{ backgroundColor: company.color || '#4f46e5' }}
                    title={company.name}
                >
                    {getInitials(company)}
                </button>
            ))}

            <div className="w-8 border-t border-gray-700 my-2"></div>

            <button
                onClick={() => navigate('/add-company')}
                className="w-12 h-12 rounded-full bg-gray-800 text-gray-400 hover:text-white hover:bg-gray-700 flex items-center justify-center transition-colors"
                title="Add Workspace"
            >
                <Plus size={24} />
            </button>
        </div>
    );
};
