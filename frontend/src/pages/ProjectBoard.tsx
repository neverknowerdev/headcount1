import React, { useState, useEffect, useCallback, useRef, useMemo } from 'react';
import axios from 'axios';
import { useParams, useNavigate } from 'react-router-dom';
import { useStore } from '../store';
import { DragDropContext, Droppable, Draggable } from '@hello-pangea/dnd';
import type { DropResult } from '@hello-pangea/dnd';
import { Plus, Settings, LockKeyhole, ArrowUpRight, Link2, Search, ListTree, Columns3, SlidersHorizontal, ChevronDown } from 'lucide-react';
import { TaskModal } from '../components/TaskModal';
import { TaskHierarchy, type BoardTask } from '../components/TaskHierarchy';
import { COLUMN_LABELS, DISPLAY_ORDER, type TaskColumn } from '../utils/taskColumns';
import { latestExecutionAgents, type TaskExecutionRun } from '../utils/taskExecutionAgents';
import { useWebSocket, wsUrl } from '../useWebSocket';
import { useCoalescedCallback } from '../utils/useCoalescedCallback';
import { sortTasksByUpdated } from '../utils/taskHierarchy';

const STATUSES = ['backlog', 'to-do', 'refinement', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'done'];
const STATUS_LABELS: Record<string, string> = {
    backlog: 'Backlog', 'to-do': 'To Do', refinement: 'Refinement', 'in-progress': 'In Progress',
    blocked: 'Blocked', 'depends-on-task': 'Depends on Task', 'in-review': 'In Review', done: 'Done',
};
const DEFAULT_COLUMNS: TaskColumn[] = ['status', 'assignee', 'agent', 'project', 'relations', 'taskId', 'updated'];
type TaskView = 'hierarchy' | 'board';
interface Project { id: number; name: string }
interface Sprint { id: number; name: string }
interface Agent { id: number; name: string }
interface SavedViewSettings { version: 2; view: TaskView; columns: TaskColumn[] }

function settingsStorageKey(companyId: number | null) { return `tasks-view-v1-${companyId ?? 'none'}`; }
function readViewSettings(companyId: number | null): SavedViewSettings {
    try {
        const parsed = JSON.parse(localStorage.getItem(settingsStorageKey(companyId)) || 'null');
        if (parsed?.version === 2) {
            const view: TaskView = parsed.view === 'board' ? 'board' : 'hierarchy';
            const columns: TaskColumn[] = Array.isArray(parsed.columns) ? (parsed.columns as unknown[]).filter((c): c is TaskColumn => DISPLAY_ORDER.includes(c as TaskColumn)) : DEFAULT_COLUMNS;
            return { version: 2, view, columns: Array.from(new Set<TaskColumn>(columns)) };
        }
        if (parsed?.version === 1) {
            const legacyColumns: TaskColumn[] = Array.isArray(parsed.columns)
                ? (parsed.columns as unknown[]).filter((c): c is TaskColumn => DISPLAY_ORDER.includes(c as TaskColumn))
                : DEFAULT_COLUMNS.filter(column => column !== 'agent');
            return {
                version: 2,
                view: 'hierarchy',
                columns: Array.from(new Set<TaskColumn>([...legacyColumns, 'agent'])),
            };
        }
    } catch { /* use defaults when storage is unavailable or invalid */ }
    return { version: 2, view: 'hierarchy', columns: DEFAULT_COLUMNS };
}

const FilterMenu: React.FC<{ label: string; options: { id: number; name: string }[]; selected: number[]; onChange: (ids: number[]) => void }> = ({ label, options, selected, onChange }) => {
    const [open, setOpen] = useState(false);
    const wrapperRef = useRef<HTMLDivElement>(null);
    const buttonRef = useRef<HTMLButtonElement>(null);
    useEffect(() => {
        const dismiss = (event: MouseEvent) => { if (!wrapperRef.current?.contains(event.target as Node)) setOpen(false); };
        const onKeyDown = (event: KeyboardEvent) => {
            if (event.key === 'Escape' && open) {
                setOpen(false);
                buttonRef.current?.focus();
            }
        };
        document.addEventListener('mousedown', dismiss);
        document.addEventListener('keydown', onKeyDown);
        return () => {
            document.removeEventListener('mousedown', dismiss);
            document.removeEventListener('keydown', onKeyDown);
        };
    }, [open]);
    return <div className="relative" ref={wrapperRef}>
        <button ref={buttonRef} type="button" aria-label={label} aria-expanded={open} onClick={() => setOpen(value => !value)} className={`inline-flex h-9 items-center gap-2 rounded-lg border px-3 text-sm ${selected.length ? 'border-indigo-200 bg-indigo-50 text-indigo-700' : 'border-slate-200 bg-white text-slate-600 hover:bg-slate-50'}`}>
            {label}{selected.length > 0 && <span className="rounded-full bg-indigo-100 px-1.5 text-[11px]">{selected.length}</span>}<ChevronDown size={14} />
        </button>
        {open && <div role="group" aria-label={`${label} filter`} className="absolute left-0 top-11 z-20 max-h-72 min-w-52 overflow-auto rounded-lg border border-slate-200 bg-white p-2 shadow-xl">
            {options.length ? options.map(option => <label key={option.id} className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm text-slate-700 hover:bg-slate-50">
                <input type="checkbox" checked={selected.includes(option.id)} onChange={event => onChange(event.target.checked ? [...selected, option.id] : selected.filter(id => id !== option.id))} className="rounded border-slate-300 text-indigo-600 focus:ring-indigo-500" />{option.name}
            </label>) : <p className="px-2 py-2 text-sm text-slate-400">No {label.toLowerCase()} available</p>}
            {selected.length > 0 && <button type="button" onClick={() => onChange([])} className="mt-1 w-full border-t border-slate-100 px-2 pt-2 text-left text-xs text-indigo-600 hover:text-indigo-800">Clear selection</button>}
        </div>}
    </div>;
};

export const ProjectBoard: React.FC = () => {
  const { shortName } = useParams<{shortName: string}>();
  const prefix = shortName ? shortName.toUpperCase() : 'T';
  const navigate = useNavigate();
  const { selectedCompanyId } = useStore();
  const [projects, setProjects] = useState<Project[]>([]);
  const [sprints, setSprints] = useState<Sprint[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [executionAgents, setExecutionAgents] = useState<Map<number, string>>(() => new Map());
  const [metadataCompanyId, setMetadataCompanyId] = useState<number | null>(null);
  const [executionAgentsCompanyId, setExecutionAgentsCompanyId] = useState<number | null>(null);
  const [dataCompanyId, setDataCompanyId] = useState<number | null>(null);
  const [selectedProjectIds, setSelectedProjectIds] = useState<number[]>([]);
  const [selectedSprintIds, setSelectedSprintIds] = useState<number[]>([]);
  const [showArchived, setShowArchived] = useState(false);
  const [search, setSearch] = useState('');
  const [tasks, setTasks] = useState<BoardTask[]>([]);
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);
  const [settings, setSettings] = useState<SavedViewSettings>(() => readViewSettings(selectedCompanyId));
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [taskError, setTaskError] = useState('');
  const [loading, setLoading] = useState(true);
  const [settingsCompanyId, setSettingsCompanyId] = useState(selectedCompanyId);
  const settingsButtonRef = useRef<HTMLButtonElement>(null);
  const settingsPanelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    // Reset company-scoped filters and payloads when the workspace changes.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSettings(readViewSettings(selectedCompanyId));
    setSettingsCompanyId(selectedCompanyId);
    setSelectedProjectIds([]);
    setSelectedSprintIds([]);
    setTasks([]);
    setDataCompanyId(null);
    setMetadataCompanyId(null);
    setExecutionAgentsCompanyId(null);
    setLoading(true);
  }, [selectedCompanyId]);
  useEffect(() => {
      if (settingsCompanyId !== selectedCompanyId) return;
      try { localStorage.setItem(settingsStorageKey(selectedCompanyId), JSON.stringify(settings)); } catch { /* preferences remain usable for this session */ }
  }, [settings, selectedCompanyId, settingsCompanyId]);

  const fetchFiltersData = useCallback(async () => {
    if (!selectedCompanyId) return;
    const companyId = selectedCompanyId;
    try {
      const [projRes, sprintRes, agentRes] = await Promise.all([
          axios.get(`/api/projects?company_id=${companyId}`),
          axios.get(`/api/sprints?company_id=${companyId}`),
          axios.get(`/api/agents?company_id=${companyId}`),
      ]);
      if (useStore.getState().selectedCompanyId !== companyId) return;
      setProjects(projRes.data || []);
      setSprints(sprintRes.data || []);
      setAgents(agentRes.data || []);
      setMetadataCompanyId(companyId);
    } catch (e) { console.error(e); }
  }, [selectedCompanyId]);

  const fetchExecutionAgents = useCallback(async () => {
    if (!selectedCompanyId) return;
    const companyId = selectedCompanyId;
    try {
      const response = await axios.get(`/api/runs?company_id=${companyId}`);
      if (useStore.getState().selectedCompanyId !== companyId) return;
      setExecutionAgents(latestExecutionAgents((response.data || []) as TaskExecutionRun[]));
      setExecutionAgentsCompanyId(companyId);
    } catch (error) {
      console.error('Could not load task execution agents', error);
    }
  }, [selectedCompanyId]);

  // Only the newest request can update this view, including on filter changes.
  const fetchSeqRef = useRef(0);
  const fetchTasks = useCallback(async () => {
    if (!selectedCompanyId) { setTasks([]); setLoading(false); return; }
    const companyId = selectedCompanyId;
    const seq = ++fetchSeqRef.current;
    setLoading(true);
    setTaskError('');
    try {
      const params = new URLSearchParams({ company_id: String(companyId), archived: String(showArchived), include_subtasks: 'true' });
      if (selectedProjectIds.length) params.set('project_ids', selectedProjectIds.join(','));
      if (selectedSprintIds.length) params.set('sprint_ids', selectedSprintIds.join(','));
      const res = await axios.get(`/api/tasks?${params.toString()}`);
      if (seq !== fetchSeqRef.current || useStore.getState().selectedCompanyId !== companyId) return;
      setTasks(res.data || []);
      setDataCompanyId(companyId);
    } catch (e) {
      if (seq === fetchSeqRef.current) { console.error(e); setTaskError('Tasks could not be loaded.'); }
    } finally { if (seq === fetchSeqRef.current) setLoading(false); }
  }, [selectedCompanyId, selectedProjectIds, selectedSprintIds, showArchived]);

  const scheduleFetchTasks = useCoalescedCallback(fetchTasks);
  const scheduleFetchExecutionAgents = useCoalescedCallback(fetchExecutionAgents);
  // eslint-disable-next-line react-hooks/set-state-in-effect -- Fetch company metadata when its scope changes.
  useEffect(() => { fetchFiltersData(); }, [fetchFiltersData]);
  // eslint-disable-next-line react-hooks/set-state-in-effect -- Fetch execution agents when company context changes.
  useEffect(() => { fetchExecutionAgents(); }, [fetchExecutionAgents]);
  // eslint-disable-next-line react-hooks/set-state-in-effect -- Fetch task data when filters change.
  useEffect(() => { fetchTasks(); }, [fetchTasks]);
  useWebSocket(wsUrl(), (msg) => {
    if (msg.type === 'task_updated' || msg.type === 'task_created') scheduleFetchTasks();
    if (['run_started', 'run_ended', 'run_paused', 'run_status'].includes(msg.type)) scheduleFetchExecutionAgents();
  }, {
    onConnect: () => {
      fetchTasks();
      fetchExecutionAgents();
    },
  });

  const updateTaskStatus = async (id: number, status: string) => {
    try { await axios.put(`/api/tasks/${id}`, { status }); }
    catch (e) { console.error(e); fetchTasks(); }
  };
  const onDragEnd = (result: DropResult) => {
      if (!result.destination) return;
      const { source, destination, draggableId } = result;
      if (destination.droppableId === 'depends-on-task') return;
      if (source.droppableId !== destination.droppableId) {
          setTasks(prev => prev.map(t => t.id.toString() === draggableId ? { ...t, status: destination.droppableId } : t));
          updateTaskStatus(parseInt(draggableId), destination.droppableId);
      }
  };

  const viewTasks = useMemo(
    () => dataCompanyId === selectedCompanyId ? sortTasksByUpdated(tasks) : [],
    [dataCompanyId, selectedCompanyId, tasks],
  );
  const visibleTasks = useMemo(
    () => viewTasks.filter(task => !search || `${task.title} ${task.ref_key ?? ''}`.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())),
    [search, viewTasks],
  );
  const visibleRoots = visibleTasks.filter(task => task.parent_id == null);
  const view = settings.view;
  const changeView = (nextView: TaskView) => setSettings(current => ({ ...current, view: nextView }));
  const toggleColumn = (column: TaskColumn) => setSettings(current => ({ ...current, columns: current.columns.includes(column) ? current.columns.filter(item => item !== column) : DISPLAY_ORDER.filter(item => current.columns.includes(item) || item === column) }));

  useEffect(() => {
    if (!settingsOpen) return;
    const onPointer = (event: MouseEvent) => { if (!settingsPanelRef.current?.contains(event.target as Node) && !settingsButtonRef.current?.contains(event.target as Node)) setSettingsOpen(false); };
    const onKey = (event: KeyboardEvent) => { if (event.key === 'Escape') { setSettingsOpen(false); settingsButtonRef.current?.focus(); } };
    document.addEventListener('mousedown', onPointer); document.addEventListener('keydown', onKey);
    return () => { document.removeEventListener('mousedown', onPointer); document.removeEventListener('keydown', onKey); };
  }, [settingsOpen]);

  const filterDataReady = metadataCompanyId === selectedCompanyId;
  const executionAgentsReady = executionAgentsCompanyId === selectedCompanyId;
  // Keep an already rendered company's tree mounted while filters or websocket
  // refreshes are in flight so row expansion and keyboard focus survive.
  const showLoading = selectedCompanyId != null && dataCompanyId !== selectedCompanyId;
  const showRefreshing = loading && dataCompanyId === selectedCompanyId;
  return (
    <div className="flex h-full min-h-0 flex-col bg-slate-50/50 px-4 pb-4 pt-5 sm:px-6">
      <div className="relative mb-5 flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <div className="grid size-10 place-items-center rounded-xl bg-indigo-600 text-white shadow-sm">
            <ListTree size={19} />
          </div>
          <div>
            <h1 className="text-xl font-semibold tracking-tight text-slate-900">Tasks</h1>
            <p className="text-xs text-slate-500">
              {showLoading ? 'Loading tasks…' : (
                <>
                  {viewTasks.length} {viewTasks.length === 1 ? 'task' : 'tasks'}
                  {showRefreshing && <span className="ml-2 text-indigo-500">Refreshing…</span>}
                </>
              )}
            </p>
          </div>
        </div>

        <div className="flex max-w-full min-w-0 flex-wrap items-center justify-end gap-2">
          <div className="inline-flex rounded-lg border border-slate-200 bg-white p-1 shadow-sm" aria-label="Main task view">
            <button
              type="button"
              aria-pressed={view === 'hierarchy'}
              onClick={() => changeView('hierarchy')}
              className={`inline-flex h-8 items-center gap-1.5 rounded-md px-3 text-sm transition ${view === 'hierarchy' ? 'bg-slate-900 text-white shadow-sm' : 'text-slate-600 hover:bg-slate-100'}`}
            >
              <ListTree size={15} />
              Hierarchy
            </button>
            <button
              type="button"
              aria-pressed={view === 'board'}
              onClick={() => changeView('board')}
              className={`inline-flex h-8 items-center gap-1.5 rounded-md px-3 text-sm transition ${view === 'board' ? 'bg-slate-900 text-white shadow-sm' : 'text-slate-600 hover:bg-slate-100'}`}
            >
              <Columns3 size={15} />
              Board
            </button>
          </div>
          <div className="relative">
            <button
              ref={settingsButtonRef}
              type="button"
              aria-label="Display settings"
              aria-haspopup="dialog"
              aria-expanded={settingsOpen}
              onClick={() => setSettingsOpen(value => !value)}
              className="grid size-10 place-items-center rounded-lg border border-slate-200 bg-white text-slate-600 shadow-sm hover:bg-slate-50"
            >
              <SlidersHorizontal size={17} />
            </button>
            {settingsOpen && (
              <div
                ref={settingsPanelRef}
                role="dialog"
                aria-label="Display settings"
                className="fixed right-2 top-24 z-30 w-[min(16rem,calc(100vw-1rem))] rounded-xl border border-slate-200 bg-white p-4 shadow-xl sm:absolute sm:right-0 sm:top-11 sm:w-64"
              >
                <div className="mb-3 flex items-center justify-between">
                  <h2 className="text-sm font-semibold text-slate-900">Display settings</h2>
                  <span className="text-[11px] text-slate-400">Saved for this company</span>
                </div>
                <p className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-slate-400">Hierarchy columns</p>
                <div className="space-y-0.5">
                  {DISPLAY_ORDER.map(column => (
                    <label key={column} className="flex cursor-pointer items-center justify-between rounded-md px-2 py-1.5 text-sm text-slate-700 hover:bg-slate-50">
                      {COLUMN_LABELS[column]}
                      <input
                        type="checkbox"
                        checked={settings.columns.includes(column)}
                        onChange={() => toggleColumn(column)}
                        aria-label={COLUMN_LABELS[column]}
                        className="size-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
                      />
                    </label>
                  ))}
                </div>
              </div>
            )}
          </div>
          <button
            onClick={() => window.location.href = `/companies/${shortName}/sprints`}
            className="inline-flex h-10 items-center gap-2 rounded-lg border border-slate-200 bg-white px-3 text-sm text-slate-600 shadow-sm hover:bg-slate-50"
          >
            <Settings size={15} />
            Manage Sprints
          </button>
          <button
            onClick={() => setIsCreateModalOpen(true)}
            className="inline-flex h-10 items-center gap-2 rounded-lg bg-indigo-600 px-4 text-sm font-medium text-white shadow-sm hover:bg-indigo-700"
          >
            <Plus size={16} />
            New Task
          </button>
        </div>
      </div>

      <div className="mb-4 flex flex-wrap items-center gap-2 rounded-xl border border-slate-200 bg-white p-2.5 shadow-sm">
        <label className="relative min-w-[190px] flex-1 sm:max-w-md">
          <Search size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
          <input
            value={search}
            onChange={event => setSearch(event.target.value)}
            placeholder="Search tasks..."
            aria-label="Search tasks"
            className="h-9 w-full rounded-lg border border-slate-200 bg-slate-50 pl-9 pr-3 text-sm text-slate-800 outline-none transition placeholder:text-slate-400 focus:border-indigo-300 focus:bg-white focus:ring-2 focus:ring-indigo-100"
          />
        </label>
        <div className="h-6 border-l border-slate-200" />
        <FilterMenu
          label="Projects"
          options={filterDataReady ? projects : []}
          selected={selectedProjectIds}
          onChange={setSelectedProjectIds}
        />
        <FilterMenu
          label="Sprints"
          options={filterDataReady ? sprints : []}
          selected={selectedSprintIds}
          onChange={setSelectedSprintIds}
        />
        <label className="inline-flex h-9 cursor-pointer items-center gap-2 rounded-lg px-2 text-sm text-slate-600 hover:bg-slate-50">
          <input
            type="checkbox"
            checked={showArchived}
            onChange={event => setShowArchived(event.target.checked)}
            className="rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
          />
          Show Archived
        </label>
      </div>

      {taskError ? (
        <div role="alert" className="grid flex-1 place-items-center rounded-xl border border-rose-200 bg-white p-8 text-center">
          <div>
            <p className="mb-3 text-sm text-rose-700">{taskError}</p>
            <button onClick={fetchTasks} className="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700">
              Retry
            </button>
          </div>
        </div>
      ) : showLoading ? (
        <div role="status" className="grid flex-1 place-items-center rounded-xl border border-slate-200 bg-white text-sm text-slate-500">
          Loading tasks…
        </div>
      ) : view === 'hierarchy' ? (
        <TaskHierarchy
          tasks={viewTasks}
          prefix={prefix}
          columns={settings.columns}
          projects={filterDataReady ? projects : []}
          sprints={filterDataReady ? sprints : []}
          agents={filterDataReady ? agents : []}
          executionAgents={executionAgentsReady ? executionAgents : new Map()}
          search={search}
          taskHref={id => `/companies/${shortName}/tasks/${id}`}
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-x-auto overflow-y-hidden">
          <DragDropContext onDragEnd={onDragEnd}>
            <div className="flex h-full min-w-max items-start gap-4 pb-4">
              {STATUSES.map(status => (
                <div key={status} className="flex max-h-full w-72 flex-col rounded-xl border border-slate-200 bg-white shadow-sm">
                  <div className="flex items-center justify-between border-b border-slate-100 px-3.5 py-3">
                    <h3 className="text-xs font-semibold uppercase tracking-wider text-slate-600">
                      {STATUS_LABELS[status] || status}
                    </h3>
                    <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs text-slate-500">
                      {visibleRoots.filter(task => task.status === status).length}
                    </span>
                  </div>
                  <Droppable droppableId={status}>
                    {(provided, snapshot) => (
                      <div
                        ref={provided.innerRef}
                        {...provided.droppableProps}
                        className={`min-h-[150px] flex-1 space-y-3 overflow-y-auto p-3 transition-colors ${snapshot.isDraggingOver ? 'bg-indigo-50' : ''}`}
                      >
                        {visibleRoots.filter(task => task.status === status).map((task, index) => (
                          <Draggable key={task.id} draggableId={task.id.toString()} index={index}>
                            {(drag, dragging) => (
                              <div
                                ref={drag.innerRef}
                                {...drag.draggableProps}
                                {...drag.dragHandleProps}
                                onClick={() => navigate(`/companies/${shortName}/tasks/${task.id}`)}
                                className={`cursor-grab rounded-lg border bg-white p-4 transition ${dragging.isDragging ? 'border-indigo-400 shadow-lg ring-2 ring-indigo-100' : 'border-slate-200 shadow-sm hover:border-indigo-300'}`}
                              >
                                <p className="text-sm font-medium text-slate-800">{task.title}</p>
                                {task.relation_summary?.blocked_by?.length ? (
                                  <div
                                    className="mt-2 flex items-center gap-1 truncate text-[11px] text-amber-700"
                                    title={task.relation_summary.blocked_by.map(item => item.title).join(', ')}
                                  >
                                    <LockKeyhole size={12} />
                                    <span>
                                      Blocked by {task.relation_summary.blocked_by.slice(0, 2).map(item => item.ref_key || `#${item.id}`).join(', ')}
                                    </span>
                                  </div>
                                ) : null}
                                {task.relation_summary?.blocks?.length ? (
                                  <div
                                    className="mt-1 flex items-center gap-1 truncate text-[11px] text-slate-500"
                                    title={task.relation_summary.blocks.map(item => item.title).join(', ')}
                                  >
                                    <ArrowUpRight size={12} />
                                    <span>
                                      Blocks {task.relation_summary.blocks.slice(0, 2).map(item => item.ref_key || `#${item.id}`).join(', ')}
                                    </span>
                                  </div>
                                ) : null}
                                {task.relation_summary?.related_to?.length ? (
                                  <div
                                    className="mt-1 flex items-center gap-1 truncate text-[11px] text-slate-400"
                                    title={task.relation_summary.related_to.map(item => item.title).join(', ')}
                                  >
                                    <Link2 size={12} />
                                    <span>
                                      Related {task.relation_summary.related_to.slice(0, 2).map(item => item.ref_key || `#${item.id}`).join(', ')}
                                    </span>
                                  </div>
                                ) : null}
                                <div className="mt-4 flex items-center justify-between">
                                  <span className="text-xs text-slate-400">{task.ref_key || `${prefix}-${task.id}`}</span>
                                  {task.priority !== 'Normal' && (
                                    <span className={`rounded-full px-2 py-0.5 text-[10px] ${task.priority === 'Urgent' ? 'bg-rose-50 text-rose-700' : task.priority === 'High' ? 'bg-orange-50 text-orange-700' : 'bg-blue-50 text-blue-700'}`}>
                                      {task.priority}
                                    </span>
                                  )}
                                </div>
                              </div>
                            )}
                          </Draggable>
                        ))}
                        {provided.placeholder}
                      </div>
                    )}
                  </Droppable>
                </div>
              ))}
            </div>
          </DragDropContext>
        </div>
      )}
      {isCreateModalOpen && <TaskModal onClose={() => setIsCreateModalOpen(false)} onTaskCreated={fetchTasks} />}
    </div>
  );
};
