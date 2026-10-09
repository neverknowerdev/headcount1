import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams } from 'react-router-dom';
import { useStore } from '../store';
import { RunTree } from '../components/RunTree';
import type { RunTreeData } from '../components/RunTree';
import { useWebSocket, wsUrl } from '../useWebSocket';
import { useCoalescedCallback } from '../utils/useCoalescedCallback';

// Run Logs lists the executor sessions of a company under the tasks they
// worked on: every top-level task with its subtasks beneath it and each
// task's sessions. Opening a session shows its full log.
export const RunLogs: React.FC = () => {
    const { selectedCompanyId } = useStore();
    const { shortName } = useParams<{ shortName: string }>();
    const [data, setData] = useState<RunTreeData>({ runs: [], tasks: [] });
    const [loading, setLoading] = useState(true);

    const fetchRuns = useCallback(async () => {
        if (!selectedCompanyId) return;
        try {
            // Overview fields only (no transcripts), so the list renders at
            // once however long the history is. A session's log is fetched
            // when it is opened.
            const res = await axios.get(`/api/runs?company_id=${selectedCompanyId}&tree=true`);
            setData({ runs: res.data?.runs || [], tasks: res.data?.tasks || [] });
        } catch (e) {
            console.error(e);
        } finally {
            setLoading(false);
        }
    }, [selectedCompanyId]);

    // eslint-disable-next-line react-hooks/set-state-in-effect -- Load the sessions when the company changes.
    useEffect(() => { fetchRuns(); }, [fetchRuns]);

    const scheduleFetch = useCoalescedCallback(fetchRuns);
    useWebSocket(wsUrl(), (msg) => {
        // A session starting or ending, or a task changing state, changes a line of the tree.
        if (['run_started', 'run_ended', 'run_paused', 'task_updated'].includes(msg.type)) scheduleFetch();
    }, {
        enabled: !!selectedCompanyId,
        // Re-fetch on every (re)connect so what was missed while offline is recovered.
        onConnect: fetchRuns,
    });

    return (
        <div className="h-full flex flex-col">
            <div className="mb-6">
                <h1 className="text-2xl font-bold">Run Logs</h1>
                <p className="text-sm text-gray-500">Executor sessions, the cheap-model runs that do a task's work, under the tasks they belong to. A task's decisions are in its own journal.</p>
            </div>
            <div className="flex-1 bg-white p-6 rounded-lg shadow border overflow-y-auto">
                {loading ? (
                    <p className="text-sm italic text-gray-500">Loading sessions…</p>
                ) : (
                    <RunTree data={data} companyPath={`/companies/${shortName}`} />
                )}
            </div>
        </div>
    );
};
