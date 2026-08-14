import React, { useEffect, useMemo, useState } from 'react';
import axios from 'axios';
import { Link2, Plus, X } from 'lucide-react';
import { useWebSocket, wsUrl } from '../useWebSocket';

interface RelationTask {
    id: number;
    ref_key?: string;
    title: string;
    status: string;
}

interface RelationView {
    relation_id: number;
    task: RelationTask;
}

interface RelationResponse {
    depends_on: RelationView[];
    blocked_by: RelationView[];
    blocks: RelationView[];
    related_to: RelationView[];
}

interface TaskRelationsProps {
    taskId: number;
    allTasks: RelationTask[];
}

const labels: Record<keyof RelationResponse, string> = {
    depends_on: 'Depends on',
    blocked_by: 'Blocked by',
    blocks: 'Blocks',
    related_to: 'Related',
};

const statusLabels: Record<string, string> = {
    'to-do': 'To Do',
    'in-progress': 'In Progress',
    'in-review': 'In Review',
    'depends-on-task': 'Depends on Task',
    blocked: 'Blocked',
    done: 'Done',
    backlog: 'Backlog',
};

export const TaskRelations: React.FC<TaskRelationsProps> = ({ taskId, allTasks }) => {
    const [relations, setRelations] = useState<RelationResponse>({ depends_on: [], blocked_by: [], blocks: [], related_to: [] });
    const [relationType, setRelationType] = useState<'depends_on' | 'blocks' | 'related_to'>('depends_on');
    const [targetID, setTargetID] = useState('');
    const [saving, setSaving] = useState(false);

    const load = async () => {
        try {
            const response = await axios.get(`/api/tasks/${taskId}/relations`);
            setRelations({
                depends_on: response.data?.depends_on || [],
                blocked_by: response.data?.blocked_by || [],
                blocks: response.data?.blocks || [],
                related_to: response.data?.related_to || [],
            });
        } catch (error) {
            console.error(error);
        }
    };

    // The task ID is the lifecycle boundary; load is intentionally local to
    // keep relation refreshes independent from the parent task form.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    useEffect(() => { load(); }, [taskId]);

    useWebSocket(wsUrl(), (message) => {
        if (message.type === 'task_updated' && message.payload?.id === taskId) {
            load();
        }
    }, { enabled: true });

    const candidates = useMemo(() => allTasks.filter(task => task.id !== taskId), [allTasks, taskId]);

    const addRelation = async () => {
        if (!targetID) return;
        setSaving(true);
        try {
            await axios.post(`/api/tasks/${taskId}/relations`, { type: relationType, task_id: Number(targetID) });
            setTargetID('');
            await load();
        } catch (error: any) {
            alert(error.response?.data?.error || 'Failed to add relation');
        } finally {
            setSaving(false);
        }
    };

    const removeRelation = async (relationID: number) => {
        try {
            await axios.delete(`/api/tasks/${taskId}/relations/${relationID}`);
            await load();
        } catch (error: any) {
            alert(error.response?.data?.error || 'Failed to remove relation');
        }
    };

    const groups: Array<keyof RelationResponse> = ['blocked_by', 'depends_on', 'blocks', 'related_to'];
    return (
        <div className="rounded-lg border border-gray-200 bg-gray-50/70 p-3 space-y-3">
            <div className="flex items-center gap-2">
                <Link2 size={15} className="text-gray-500" />
                <span className="text-sm font-medium text-gray-700">Task relations</span>
            </div>
            {groups.map(group => relations[group].length > 0 && (
                <div key={group} className="space-y-1">
                    <div className={`text-[11px] font-semibold uppercase tracking-wide ${group === 'blocked_by' ? 'text-amber-700' : 'text-gray-500'}`}>
                        {labels[group]}
                    </div>
                    {relations[group].map(relation => (
                        <div key={`${group}-${relation.relation_id}`} className="flex items-center gap-1.5 text-xs bg-white border rounded px-2 py-1">
                            <span className="font-mono text-gray-500">{relation.task.ref_key || `#${relation.task.id}`}</span>
                            <span className="truncate flex-1" title={relation.task.title}>{relation.task.title}</span>
                            <span className="text-[10px] text-gray-400">{statusLabels[relation.task.status] || relation.task.status}</span>
                            {group !== 'blocked_by' && (
                                <button type="button" onClick={() => removeRelation(relation.relation_id)} className="text-gray-400 hover:text-red-600" title="Remove relation">
                                    <X size={12} />
                                </button>
                            )}
                        </div>
                    ))}
                </div>
            ))}
            <div className="flex gap-1.5 pt-1">
                <select value={relationType} onChange={event => setRelationType(event.target.value as typeof relationType)} className="border rounded px-1.5 py-1 text-xs bg-white">
                    <option value="depends_on">Depends on</option>
                    <option value="blocks">Blocks</option>
                    <option value="related_to">Related</option>
                </select>
                <select value={targetID} onChange={event => setTargetID(event.target.value)} className="min-w-0 flex-1 border rounded px-1.5 py-1 text-xs bg-white">
                    <option value="">Select task…</option>
                    {candidates.map(task => <option key={task.id} value={task.id}>{task.ref_key || `#${task.id}`} — {task.title}</option>)}
                </select>
                <button type="button" onClick={addRelation} disabled={!targetID || saving} className="inline-flex items-center justify-center rounded bg-indigo-600 text-white px-2 disabled:opacity-50" title="Add relation">
                    <Plus size={14} />
                </button>
            </div>
        </div>
    );
};

export default TaskRelations;
