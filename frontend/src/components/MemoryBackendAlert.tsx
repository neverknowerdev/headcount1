import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useLocation } from 'react-router-dom';

interface MemoryStatus {
    available: boolean;
    url?: string;
    schema?: string;
    error?: string;
    notice?: string;
}

// App-wide banner for the long-term memory backend (hindsight-api), rendered
// by Layout on every page:
//  - red when the backend failed to start or died (status.error) — agents
//    run without memory until it's fixed;
//  - amber when it is up but degraded (status.notice), e.g. memory was
//    re-initialized on a fallback schema after an incompatible-migration
//    crash.
// Nothing is shown while the backend is merely still booting (unavailable
// but no error yet) so normal startup never flashes a false alarm.
export const MemoryBackendAlert: React.FC = () => {
    // null until the first fetch resolves, so the banner never flashes.
    const [status, setStatus] = useState<MemoryStatus | null>(null);
    const location = useLocation();

    const check = useCallback(async () => {
        try {
            const res = await axios.get('/api/memory/status');
            setStatus(res.data);
        } catch {
            // Can't tell — don't nag.
            setStatus(null);
        }
    }, []);

    useEffect(() => {
        check();
    }, [check, location.pathname]);

    // Startup can take minutes and the backend can die at any point — keep
    // the status fresh without waiting for a navigation.
    useEffect(() => {
        const id = setInterval(check, 30000);
        return () => clearInterval(id);
    }, [check]);

    if (!status) return null;

    if (!status.available && status.error) {
        return (
            <div className="mb-4 p-3 rounded text-sm bg-red-50 text-red-800 border border-red-200">
                <span className="font-semibold">Long-term memory is not working</span> — agent runs will proceed without
                memory recall or retention.
                <div className="mt-1 font-mono text-xs break-all">{status.error}</div>
            </div>
        );
    }

    if (status.available && status.notice) {
        return (
            <div className="mb-4 p-3 rounded text-sm bg-amber-50 text-amber-800 border border-amber-200">
                {status.notice}
            </div>
        );
    }

    return null;
};
