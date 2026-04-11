import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { useParams } from 'react-router-dom';

const STATUSES = ['backlog', 'to-do', 'refinement', 'in-progress', 'in-review', 'blocked', 'done'];

export const ProjectBoard: React.FC = () => {
  const { projectId } = useParams();
  const [tasks, setTasks] = useState<any[]>([]);
  const [newTaskTitle, setNewTaskTitle] = useState('');

  const fetchTasks = async () => {
    try {
      const res = await axios.get(`/api/tasks?project_id=${projectId}`);
      setTasks(res.data || []);
    } catch (e) {
      console.error(e);
    }
  };

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
  }, [projectId]);

  const createTask = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newTaskTitle) return;
    try {
      await axios.post('/api/tasks', {
        project_id: parseInt(projectId || '0'),
        title: newTaskTitle
      });
      setNewTaskTitle('');
    } catch (e) {
      console.error(e);
    }
  };

  const updateTaskStatus = async (id: number, status: string) => {
    try {
      await axios.put(`/api/tasks/${id}/status`, { status });
    } catch (e) {
      console.error(e);
    }
  };

  return (
    <div className="h-full flex flex-col">
      <div className="flex justify-between items-center mb-6">
        <h1 className="text-2xl font-bold">Project Board</h1>
        <form onSubmit={createTask} className="flex gap-2">
          <input
            type="text"
            value={newTaskTitle}
            onChange={(e) => setNewTaskTitle(e.target.value)}
            placeholder="New Task Title"
            className="border p-2 rounded text-sm"
          />
          <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded text-sm">
            Add Task
          </button>
        </form>
      </div>

      <div className="flex-1 overflow-x-auto">
        <div className="flex gap-4 min-w-max pb-4 h-full">
          {STATUSES.map(status => (
            <div key={status} className="w-80 bg-gray-50 rounded-lg p-4 flex flex-col">
              <h3 className="font-semibold text-gray-700 uppercase text-xs mb-4">{status}</h3>
              <div className="flex-1 overflow-y-auto space-y-3">
                {tasks.filter(t => t.status === status).map(task => (
                  <div key={task.id} className="bg-white p-4 rounded border shadow-sm cursor-pointer hover:border-indigo-300">
                    <p className="font-medium text-sm">{task.title}</p>
                    <div className="mt-4 flex gap-2 overflow-x-auto pb-2">
                      {STATUSES.filter(s => s !== status).map(s => (
                        <button
                          key={s}
                          onClick={() => updateTaskStatus(task.id, s)}
                          className="text-xs px-2 py-1 bg-gray-100 rounded hover:bg-gray-200 whitespace-nowrap"
                        >
                          → {s}
                        </button>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
};
