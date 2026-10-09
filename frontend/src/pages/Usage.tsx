import React, { useState } from 'react';
import { useStore } from '../store';
import { UsagePanel } from '../components/UsagePanel';

const RANGES: { key: string; label: string; days: number | null }[] = [
    { key: '1', label: 'Today', days: 1 },
    { key: '7', label: 'Last 7 days', days: 7 },
    { key: '30', label: 'Last 30 days', days: 30 },
    { key: 'all', label: 'All time', days: null },
];

// The first day of a range, as the date the API filters from.
function rangeStart(days: number | null): string {
    if (days === null) return '';
    const start = new Date();
    start.setDate(start.getDate() - (days - 1));
    return start.toISOString().slice(0, 10);
}

// Usage shows what the company's tasks cost in model calls: overall, and
// broken down by task, by workflow step, by agent and by model. Every row
// opens into its calls, and every call into its log.
export const Usage: React.FC = () => {
    const { selectedCompanyId } = useStore();
    const [range, setRange] = useState('7');
    if (!selectedCompanyId) return <p className="text-sm text-gray-500">Select a company.</p>;

    const from = rangeStart(RANGES.find(r => r.key === range)?.days ?? null);
    const filter = `company_id=${selectedCompanyId}${from ? `&from=${from}` : ''}`;

    return (
        <div className="space-y-5">
            <div className="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h1 className="text-2xl font-bold">Usage</h1>
                    <p className="text-sm text-gray-500">Every model call made for this company's tasks. Smart models decide; cheap models do the work.</p>
                </div>
                <div className="inline-flex overflow-hidden rounded-lg border bg-white text-sm" role="group" aria-label="Period">
                    {RANGES.map(r => (
                        <button
                            key={r.key}
                            onClick={() => setRange(r.key)}
                            className={`px-3 py-1.5 ${range === r.key ? 'bg-indigo-600 text-white' : 'text-gray-600 hover:bg-gray-50'}`}
                        >
                            {r.label}
                        </button>
                    ))}
                </div>
            </div>
            <UsagePanel
                reportUrl={`/api/usage?${filter}&group_by=root_task,phase,phase_tier,agent,model`}
                callsQuery={filter}
                dimensions={['root_task', 'phase', 'agent', 'model']}
            />
        </div>
    );
};
