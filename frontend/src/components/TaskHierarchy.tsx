import React, { useMemo, useState } from 'react';
import { ChevronDown, ChevronRight, Circle, CircleCheck, CircleDashed, Link2 } from 'lucide-react';
import { Link } from 'react-router-dom';
import { buildTaskForest, getSearchVisibility, sortTasksByUpdated, type HierarchyTask, type TaskNode } from '../utils/taskHierarchy';
import { COLUMN_LABELS, DISPLAY_ORDER, type TaskColumn } from '../utils/taskColumns';

export interface BoardTask extends HierarchyTask {
  description: string;
  status: string;
  priority: string;
  company_id?: number;
  project_id?: number | null;
  sprint_id?: number | null;
  agent_id?: number | null;
  due_date?: string | null;
  updated_at?: string;
  relation_summary?: { blocked_by?: HierarchyTask[]; blocks?: HierarchyTask[]; related_to?: HierarchyTask[] };
}

export interface TaskHierarchyProps {
  tasks: BoardTask[];
  prefix: string;
  columns: TaskColumn[];
  projects: { id: number; name: string }[];
  sprints: { id: number; name: string }[];
  agents: { id: number; name: string }[];
  executionAgents: Map<number, string>;
  search: string;
  taskHref: (id: number) => string;
}

const STATUS_LABELS: Record<string, string> = {
  backlog: 'Backlog',
  'to-do': 'To do',
  refinement: 'Refinement',
  'in-progress': 'In progress',
  blocked: 'Blocked',
  'depends-on-task': 'Depends on task',
  'in-review': 'In review',
  done: 'Done',
};
const STATUS_STYLES: Record<string, string> = {
  backlog: 'bg-slate-100 text-slate-700',
  'to-do': 'bg-sky-50 text-sky-700',
  refinement: 'bg-violet-50 text-violet-700',
  'in-progress': 'bg-indigo-50 text-indigo-700',
  blocked: 'bg-rose-50 text-rose-700',
  'depends-on-task': 'bg-amber-50 text-amber-700',
  'in-review': 'bg-cyan-50 text-cyan-700',
  done: 'bg-emerald-50 text-emerald-700',
};
function relativeDate(value?: string | null) {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' }).format(date);
}

function relativeUpdated(value?: string) {
  if (!value) return '—';
  const timestamp = new Date(value).getTime();
  if (Number.isNaN(timestamp)) return '—';
  const seconds = Math.round((timestamp - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
  const absolute = Math.abs(seconds);
  if (absolute < 60) return formatter.format(Math.round(seconds), 'second');
  if (absolute < 3600) return formatter.format(Math.round(seconds / 60), 'minute');
  if (absolute < 86400) return formatter.format(Math.round(seconds / 3600), 'hour');
  if (absolute < 604800) return formatter.format(Math.round(seconds / 86400), 'day');
  return formatter.format(Math.round(seconds / 604800), 'week');
}

export const TaskHierarchy: React.FC<TaskHierarchyProps> = ({ tasks, prefix, columns, projects, sprints, agents, executionAgents, search, taskHref }) => {
  const orderedTasks = useMemo(() => sortTasksByUpdated(tasks), [tasks]);
  const forest = useMemo(() => buildTaskForest(orderedTasks), [orderedTasks]);
  const { visibleIds, expandedIds: searchExpanded } = useMemo(() => getSearchVisibility(tasks, search), [tasks, search]);
  const [collapsedIds, setCollapsedIds] = useState<Set<number>>(() => new Set());
  const projectNames = useMemo(() => new Map(projects.map(item => [item.id, item.name])), [projects]);
  const sprintNames = useMemo(() => new Map(sprints.map(item => [item.id, item.name])), [sprints]);
  const agentNames = useMemo(() => new Map(agents.map(item => [item.id, item.name])), [agents]);
  const visibleColumns = DISPLAY_ORDER.filter(column => columns.includes(column));
  const toggle = (id: number) => setCollapsedIds(current => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });

  const renderNode = (node: TaskNode<BoardTask>, depth: number): React.ReactNode => {
    const { task } = node;
    if (visibleIds && !visibleIds.has(task.id)) return null;
    const hasChildren = node.children.length > 0;
    const collapsed = !searchExpanded.has(task.id) && collapsedIds.has(task.id);
    const relations =
      (task.relation_summary?.blocked_by?.length ?? 0) +
      (task.relation_summary?.blocks?.length ?? 0) +
      (task.relation_summary?.related_to?.length ?? 0);
    return <React.Fragment key={task.id}>
      <tr data-testid={`task-row-${task.id}`} data-depth={depth} className="group border-b border-slate-100 last:border-0 hover:bg-slate-50/80">
        <td className="relative z-[1] w-[280px] max-w-[280px] bg-white px-3 py-3 group-hover:bg-slate-50/80 sm:sticky sm:left-0 sm:w-[420px] sm:max-w-[420px] sm:px-4">
          <div className="flex min-w-0 items-center gap-2" style={{ paddingLeft: `${depth * 27}px` }}>
            {depth > 0 && (
              <span
                aria-hidden="true"
                className="pointer-events-none absolute inset-y-0 border-l border-slate-200"
                style={{ left: `${28 + (depth - 1) * 27}px` }}
              />
            )}
            {hasChildren ? (
              <button
                type="button"
                aria-label={`${collapsed ? 'Expand' : 'Collapse'} ${task.title}`}
                aria-expanded={!collapsed}
                onClick={() => toggle(task.id)}
                className="relative z-[1] grid size-6 shrink-0 place-items-center rounded-md text-slate-500 hover:bg-slate-200 hover:text-slate-800 focus-visible:outline focus-visible:outline-2 focus-visible:outline-indigo-500"
              >
                {collapsed ? <ChevronRight size={15} /> : <ChevronDown size={15} />}
              </button>
            ) : (
              <span className="grid size-6 shrink-0 place-items-center" aria-hidden="true">
                {task.status === 'done' ? (
                  <CircleCheck size={16} className="text-emerald-600" />
                ) : task.status === 'blocked' ? (
                  <Circle size={15} className="text-rose-500" />
                ) : (
                  <CircleDashed size={16} className="text-slate-400" />
                )}
              </span>
            )}
            <Link
              to={taskHref(task.id)}
              className="min-w-0 flex-1 truncate text-sm font-medium text-slate-800 hover:text-indigo-700"
              title={task.title}
            >
              {task.title}
            </Link>
            {node.orphanParentId && (
              <span className="shrink-0 rounded-full bg-amber-50 px-2 py-0.5 text-[11px] text-amber-700">
                Parent #{node.orphanParentId} not in view
              </span>
            )}
            {hasChildren && <span className="shrink-0 text-xs text-slate-400">{node.children.length}</span>}
          </div>
        </td>
        {visibleColumns.map(column => (
          <td key={column} className="whitespace-nowrap px-4 py-3 text-sm text-slate-600">
            {column === 'status' && (
              <span className={`rounded-full px-2.5 py-1 text-xs font-medium ${STATUS_STYLES[task.status] ?? 'bg-slate-100 text-slate-700'}`}>
                {STATUS_LABELS[task.status] ?? task.status}
              </span>
            )}
            {column === 'assignee' && (
              task.agent_id
                ? agentNames.get(task.agent_id) ?? `Agent #${task.agent_id}`
                : <span className="text-slate-400">Unassigned</span>
            )}
            {column === 'agent' && (executionAgents.get(task.id) ?? <span className="text-slate-400">Not started</span>)}
            {column === 'project' && (
              task.project_id
                ? projectNames.get(task.project_id) ?? `Project #${task.project_id}`
                : <span className="text-slate-400">No project</span>
            )}
            {column === 'sprint' && (
              task.sprint_id
                ? sprintNames.get(task.sprint_id) ?? `Sprint #${task.sprint_id}`
                : <span className="text-slate-400">No sprint</span>
            )}
            {column === 'priority' && (
              <span className={task.priority === 'Urgent' ? 'font-medium text-rose-600' : task.priority === 'High' ? 'font-medium text-orange-600' : ''}>
                {task.priority || 'Normal'}
              </span>
            )}
            {column === 'relations' && (
              relations ? (
                <div className="flex max-w-64 flex-wrap gap-1" aria-label={`${relations} task relations`}>
                  {task.relation_summary?.blocked_by?.map(peer => (
                    <span
                      key={`blocked-${peer.id}`}
                      title={`Blocked by ${peer.title}`}
                      className="inline-flex items-center gap-1 rounded-full bg-amber-50 px-2 py-1 text-[11px] text-amber-800"
                    >
                      <Link2 size={11} />
                      <span>Blocked by {peer.ref_key || `#${peer.id}`}</span>
                    </span>
                  ))}
                  {task.relation_summary?.blocks?.map(peer => (
                    <span
                      key={`blocks-${peer.id}`}
                      title={`Blocks ${peer.title}`}
                      className="inline-flex items-center gap-1 rounded-full bg-sky-50 px-2 py-1 text-[11px] text-sky-800"
                    >
                      <Link2 size={11} />
                      <span>Blocks {peer.ref_key || `#${peer.id}`}</span>
                    </span>
                  ))}
                  {task.relation_summary?.related_to?.map(peer => (
                    <span
                      key={`related-${peer.id}`}
                      title={`Related to ${peer.title}`}
                      className="inline-flex items-center gap-1 rounded-full bg-slate-100 px-2 py-1 text-[11px] text-slate-600"
                    >
                      <Link2 size={11} />
                      <span>Related {peer.ref_key || `#${peer.id}`}</span>
                    </span>
                  ))}
                </div>
              ) : (
                <span className="text-slate-400">—</span>
              )
            )}
            {column === 'taskId' && <span className="font-mono text-xs text-slate-500">{task.ref_key || `${prefix}-${task.id}`}</span>}
            {column === 'updated' && relativeUpdated(task.updated_at)}
            {column === 'dueDate' && relativeDate(task.due_date)}
          </td>
        ))}
      </tr>
      {hasChildren && !collapsed && node.children.map(child => renderNode(child, depth + 1))}
    </React.Fragment>;
  };

  if (!tasks.length) {
    return (
      <div data-testid="task-hierarchy" className="grid min-h-64 place-items-center rounded-xl border border-dashed border-slate-300 bg-white text-sm text-slate-500">
        No tasks yet. Create a task to get started.
      </div>
    );
  }
  const rows = forest.map(node => renderNode(node, 0));
  return (
    <div data-testid="task-hierarchy" className="min-h-0 flex-1 overflow-auto rounded-xl border border-slate-200 bg-white shadow-sm">
      <table className="w-full min-w-max border-collapse text-left">
        <thead className="sticky top-0 z-[2] bg-slate-50/95 backdrop-blur">
          <tr className="border-b border-slate-200">
            <th
              scope="col"
              className="static w-[280px] max-w-[280px] bg-slate-50 px-3 py-3 text-[11px] font-semibold uppercase tracking-wider text-slate-500 sm:sticky sm:left-0 sm:w-[420px] sm:max-w-[420px] sm:px-4"
            >
              Task
            </th>
            {visibleColumns.map(column => (
              <th scope="col" key={column} className="px-4 py-3 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
                {COLUMN_LABELS[column]}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{rows}</tbody>
      </table>
      {search && visibleIds?.size === 0 && (
        <div className="p-8 text-center text-sm text-slate-500">No tasks match “{search}”.</div>
      )}
    </div>
  );
};
