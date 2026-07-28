import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { Link, useLocation } from 'react-router-dom';
import { useStore } from '../store';

// App-wide banner shown while long-term memory has no Retain model
// configured (the hindsight_retain "Default Models" purpose): the memory
// backend then runs without an LLM and retain quality is degraded. Rendered
// by Layout on every page; re-checks on navigation and whenever
// DefaultModelSettings saves (via the 'default-model-settings-changed'
// window event).
export const MemoryModelAlert: React.FC = () => {
    // null until the first fetch resolves, so the banner never flashes.
    const [retainConfigured, setRetainConfigured] = useState<boolean | null>(null);
    const location = useLocation();
    const { companies, selectedCompanyId } = useStore();

    const check = useCallback(async () => {
        try {
            const res = await axios.get('/api/default-model-settings');
            const retain = (res.data || []).find((s: any) => s.purpose === 'hindsight_retain');
            setRetainConfigured(!!(retain?.provider_id || retain?.model_group_id));
        } catch {
            // Can't tell — don't nag.
            setRetainConfigured(null);
        }
    }, []);

    useEffect(() => {
        check();
    }, [check, location.pathname]);

    useEffect(() => {
        window.addEventListener('default-model-settings-changed', check);
        return () => window.removeEventListener('default-model-settings-changed', check);
    }, [check]);

    if (retainConfigured !== false) return null;

    const company = companies.find((c: any) => c.id === selectedCompanyId);
    const providersPath = company ? `/companies/${company.short_name}/providers` : null;

    return (
        <div className="mb-4 p-3 rounded text-sm bg-amber-50 text-amber-800 border border-amber-200">
            Long-term memory has no Retain model configured — the memory backend is running without an LLM, so retain quality is degraded.{' '}
            {providersPath && (
                <Link to={providersPath} className="font-medium underline hover:text-amber-900">
                    Configure it under LLM Providers → Long-term Memory (Hindsight).
                </Link>
            )}
        </div>
    );
};
