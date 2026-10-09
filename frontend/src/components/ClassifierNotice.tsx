import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { AlertTriangle } from 'lucide-react';

// What the engine does without a classifier.
const WITHOUT_CLASSIFIER =
    'Tasks still run, on fixed rules: executors checkpoint only at a set interval, a decision recorded twice is caught only when worded the same, and a session going in circles is stopped only when it repeats one call exactly.';

// ClassifierNotice warns when no classifier (a System One model) is set, and says
// what is lost without one. It shows nothing while it does not know, and
// nothing once a classifier is chosen. refreshSignal makes it look again.
export const ClassifierNotice: React.FC<{ refreshSignal?: unknown; children?: React.ReactNode }> = ({ refreshSignal, children }) => {
    const [missing, setMissing] = useState(false);

    useEffect(() => {
        let current = true;
        axios.get('/api/default-model-settings')
            .then(res => {
                if (!current) return;
                const slot = (res.data || []).find((s: { purpose: string }) => s.purpose === 'classifier');
                setMissing(!!slot && !slot.provider_id && !slot.model_group_id);
            })
            .catch(() => { if (current) setMissing(false); });
        return () => { current = false; };
    }, [refreshSignal]);

    if (!missing) return null;
    return (
        <div className="flex flex-wrap items-start gap-3 rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900" data-testid="classifier-warning">
            <AlertTriangle size={18} className="mt-0.5 shrink-0 text-amber-600" />
            <div className="min-w-0 flex-1">
                <p className="font-semibold">No classifier is set</p>
                <p className="mt-0.5">
                    {WITHOUT_CLASSIFIER} A System One model does these three things properly, for very little per call: TypeSafe's Jev, served by TypeSafe itself and by OpenCode (where jev-1.13-free costs nothing) and AI Surplus.
                </p>
            </div>
            {children && <div className="shrink-0">{children}</div>}
        </div>
    );
};
