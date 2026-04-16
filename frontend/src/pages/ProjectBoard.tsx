/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useStore } from '../store';
import { DragDropContext, Droppable, Draggable } from '@hello-pangea/dnd';
import type { DropResult } from '@hello-pangea/dnd';
import { Plus } from 'lucide-react';
import { TaskModal } from '../components/TaskModal';

const STATUSES = ['backlog', 'to-do', 'refinement', 'in-progress', 'in-review', 'blocked', 'done'];

interface Task {
    id: number;
    title: string;
    description: string;
    status: string;
}

interface Project {
    id: number;
    name: string;
}

export const ProjectBoard: React.FC = () => {
  const { selectedCompanyId } = useStore();
  const [projects, setProjects] = useState<Project[]>([]);
  const [selectedProjectId, setSelectedProjectId] = useState<number | null>(null);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);
  const [viewingTaskId, setViewingTaskId] = useState<number | null>(null);

  const fetchProjects = useCallback(async () => {
    if (!selectedCompanyId) return;
    try {
      const res = await axios.get(`/api/projects?company_id=${selectedCompanyId}`);
      const projs = res.data || [];
      setProjects(projs);
      if (projs.length > 0 && !selectedProjectId) {
          setSelectedProjectId(projs[0].id);
      }
    } catch (e) {
      console.error(e);
    }
  }, [selectedCompanyId, selectedProjectId]);

  const fetchTasks = useCallback(async () => {
    if (!selectedProjectId) return;
    try {
      const res = await axios.get(`/api/tasks?project_id=${selectedProjectId}`);
      setTasks(res.data || []);
    } catch (e) {
      console.error(e);
    }
  }, [selectedProjectId]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    fetchProjects();
  }, [fetchProjects]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
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
      await axios.put(`/api/tasks/${id}/status`, { status });
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

  if (projects.length === 0) {
      return (
          <div className="flex flex-col items-center justify-center h-full space-y-4">
              <p className="text-gray-500">No projects yet.</p>
              <button className="bg-indigo-600 text-white px-4 py-2 rounded" onClick={() => window.location.href='/projects'}>Go create a project</button>
          </div>
      );
  }

  return (
    <div className="h-full flex flex-col">
      <div className="flex justify-between items-center mb-6">
        <div className="flex items-center space-x-4">
            <h1 className="text-2xl font-bold">Tasks</h1>
            <select
                value={selectedProjectId || ''}
                onChange={e => setSelectedProjectId(parseInt(e.target.value))}
                className="border-gray-300 rounded-md text-sm pl-3 pr-8 py-2 border shadow-sm focus:ring-indigo-500 focus:border-indigo-500"
            >
                {projects.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
        </div>

        <button onClick={() => setIsCreateModalOpen(true)} className="bg-indigo-600 text-white px-4 py-2 rounded-md text-sm flex items-center shadow-sm hover:bg-indigo-700">
            <Plus size={16} className="mr-1"/> New Task
        </button>
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
                                        onClick={() => setViewingTaskId(task.id)}
                                        className={`bg-white p-4 rounded-md border shadow-sm ${snapshot.isDragging ? 'shadow-lg ring-2 ring-indigo-500 border-transparent' : 'hover:border-indigo-300'} transition-shadow cursor-grab`}
                                    >
                                        <p className="font-medium text-sm text-gray-900">{task.title}</p>
                                        <div className="mt-4 flex justify-between items-center">
                                            <span className="text-xs text-gray-400">T-{task.id}</span>
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
      {viewingTaskId && <TaskModal taskId={viewingTaskId} onClose={() => setViewingTaskId(null)} />}
      {isCreateModalOpen && <TaskModal projectId={selectedProjectId || undefined} onClose={() => setIsCreateModalOpen(false)} onTaskCreated={fetchTasks} />}
    </div>
  );
};
