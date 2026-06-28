/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useParams, useNavigate } from 'react-router-dom';
import { useStore } from '../store';
import { DragDropContext, Droppable, Draggable } from '@hello-pangea/dnd';
import type { DropResult } from '@hello-pangea/dnd';
import { Plus, Settings } from 'lucide-react';
import { TaskModal } from '../components/TaskModal';

const STATUSES = ['backlog', 'to-do', 'refinement', 'in-progress', 'in-review', 'blocked', 'done'];

interface Task {
    id: number;
    title: string;
    description: string;
    status: string;
    priority: string;
}

interface Project {
    id: number;
    name: string;
}

interface Sprint {
    id: number;
    name: string;
}

export const ProjectBoard: React.FC = () => {
    const { shortName } = useParams<{shortName: string}>();
    const prefix = shortName ? shortName.toUpperCase() : 'T';
    const navigate = useNavigate();
  const { selectedCompanyId } = useStore();
  const [projects, setProjects] = useState<Project[]>([]);
  const [sprints, setSprints] = useState<Sprint[]>([]);

  const [selectedProjectIds, setSelectedProjectIds] = useState<number[]>([]);
  const [selectedSprintIds, setSelectedSprintIds] = useState<number[]>([]);
  const [showArchived, setShowArchived] = useState(false);

  const [tasks, setTasks] = useState<Task[]>([]);
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);

  const fetchFiltersData = useCallback(async () => {
    if (!selectedCompanyId) return;
    try {
      const [projRes, sprintRes] = await Promise.all([
          axios.get(`/api/projects?company_id=${selectedCompanyId}`),
          axios.get(`/api/sprints?company_id=${selectedCompanyId}`)
      ]);
      setProjects(projRes.data || []);
      setSprints(sprintRes.data || []);
    } catch (e) {
      console.error(e);
    }
  }, [selectedCompanyId]);

  const fetchTasks = useCallback(async () => {
    if (!selectedCompanyId) return;
    try {
      let url = `/api/tasks?company_id=${selectedCompanyId}&archived=${showArchived}`;
      if (selectedProjectIds.length > 0) {
          url += `&project_ids=${selectedProjectIds.join(',')}`;
      }
      if (selectedSprintIds.length > 0) {
          url += `&sprint_ids=${selectedSprintIds.join(',')}`;
      }

      const res = await axios.get(url);
      setTasks(res.data || []);
    } catch (e) {
      console.error(e);
    }
  }, [selectedCompanyId, selectedProjectIds, selectedSprintIds, showArchived]);

  useEffect(() => {
    fetchFiltersData();
  }, [fetchFiltersData]);

  useEffect(() => {
    fetchTasks();
    const ws = new WebSocket(`ws://${window.location.host}/api/ws`);
    ws.onmessage = (event) => {
      const msg = JSON.parse(event.data);
      if (msg.type === 'task_updated' || msg.type === 'task_created') {
        fetchTasks();
      }
    };
    return () => ws.close();
  }, [fetchTasks]);

  const updateTaskStatus = async (id: number, status: string) => {
    try {
      await axios.put(`/api/tasks/${id}`, { status });
    } catch (e) {
      console.error(e);
      fetchTasks(); // rollback on error
    }
  };

  const onDragEnd = (result: DropResult) => {
      if (!result.destination) return;

      const { source, destination, draggableId } = result;
      if (source.droppableId !== destination.droppableId) {
          // Optimistic UI update
          setTasks(prev => prev.map(t =>
              t.id.toString() === draggableId ? { ...t, status: destination.droppableId } : t
          ));
          updateTaskStatus(parseInt(draggableId), destination.droppableId);
      }
  };

  return (
    <div className="h-full flex flex-col">
      <div className="flex flex-col md:flex-row justify-between items-start md:items-center mb-6 space-y-4 md:space-y-0">
        <div className="flex items-center space-x-4">
            <h1 className="text-2xl font-bold">Tasks</h1>
            <div className="flex space-x-2 text-sm">
                <select
                    multiple
                    size={1}
                    value={selectedProjectIds.map(String)}
                    onChange={e => {
                        const values = Array.from(e.target.selectedOptions, option => parseInt(option.value));
                        setSelectedProjectIds(values);
                    }}
                    className="border-gray-300 rounded-md py-1 px-2 border shadow-sm"
                    title="Filter by Projects (Hold Ctrl/Cmd to select multiple)"
                >
                    <option value="" disabled>-- Projects --</option>
                    {projects.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                </select>

                <select
                    multiple
                    size={1}
                    value={selectedSprintIds.map(String)}
                    onChange={e => {
                        const values = Array.from(e.target.selectedOptions, option => parseInt(option.value));
                        setSelectedSprintIds(values);
                    }}
                    className="border-gray-300 rounded-md py-1 px-2 border shadow-sm"
                    title="Filter by Sprints (Hold Ctrl/Cmd to select multiple)"
                >
                    <option value="" disabled>-- Sprints --</option>
                    {sprints.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}
                </select>

                <label className="flex items-center space-x-1 cursor-pointer">
                    <input type="checkbox" checked={showArchived} onChange={e => setShowArchived(e.target.checked)} className="rounded text-indigo-600" />
                    <span>Show Archived</span>
                </label>
            </div>
        </div>

        <div className="flex gap-2">
            <button onClick={() => window.location.href=`/companies/${shortName}/sprints`} className="bg-gray-100 text-gray-700 px-4 py-2 rounded-md text-sm flex items-center border hover:bg-gray-200">
                <Settings size={16} className="mr-1"/> Manage Sprints
            </button>
            <button onClick={() => setIsCreateModalOpen(true)} className="bg-indigo-600 text-white px-4 py-2 rounded-md text-sm flex items-center shadow-sm hover:bg-indigo-700">
                <Plus size={16} className="mr-1"/> New Task
            </button>
        </div>
      </div>

      <div className="flex-1 overflow-x-auto overflow-y-hidden">
        <DragDropContext onDragEnd={onDragEnd}>
            <div className="flex gap-4 min-w-max pb-4 h-full items-start">
            {STATUSES.map(status => (
                <div key={status} className="w-72 bg-gray-100 rounded-lg flex flex-col max-h-full">
                <div className="p-3 border-b border-gray-200">
                    <h3 className="font-semibold text-gray-700 uppercase text-xs tracking-wider flex justify-between">
                        {status.replace('-', ' ')}
                        <span className="bg-gray-200 text-gray-600 px-2 py-0.5 rounded-full text-xs">
                            {tasks.filter(t => t.status === status).length}
                        </span>
                    </h3>
                </div>

                <Droppable droppableId={status}>
                    {(provided, snapshot) => (
                    <div
                        ref={provided.innerRef}
                        {...provided.droppableProps}
                        className={`flex-1 overflow-y-auto p-3 space-y-3 min-h-[150px] transition-colors ${snapshot.isDraggingOver ? 'bg-indigo-50' : ''}`}
                    >
                        {tasks.filter(t => t.status === status).map((task, index) => (
                            <Draggable key={task.id} draggableId={task.id.toString()} index={index}>
                                {(provided, snapshot) => (
                                    <div
                                        ref={provided.innerRef}
                                        {...provided.draggableProps}
                                        {...provided.dragHandleProps}
                                        onClick={() => navigate(`/companies/${shortName}/tasks/${task.id}`)}
                                        className={`bg-white p-4 rounded-md border shadow-sm ${snapshot.isDragging ? 'shadow-lg ring-2 ring-indigo-500 border-transparent' : 'hover:border-indigo-300'} transition-shadow cursor-grab`}
                                    >
                                        <p className="font-medium text-sm text-gray-900">{task.title}</p>
                                        <div className="mt-4 flex justify-between items-center">
                                            <span className="text-xs text-gray-400">{prefix}-{task.id}</span>
                                            {task.priority !== 'Normal' && (
                                                <span className={`text-[10px] px-2 py-0.5 rounded-full ${
                                                    task.priority === 'Urgent' ? 'bg-red-100 text-red-800' :
                                                    task.priority === 'High' ? 'bg-orange-100 text-orange-800' :
                                                    'bg-blue-100 text-blue-800'
                                                }`}>
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
      {isCreateModalOpen && <TaskModal onClose={() => setIsCreateModalOpen(false)} onTaskCreated={fetchTasks} />}
    </div>
  );
};
