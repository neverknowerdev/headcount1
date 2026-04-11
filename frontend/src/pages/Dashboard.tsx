/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { useStore } from '../store';
import { Activity, CheckCircle, Users } from 'lucide-react';

interface ActivityLog {
    id: number;
    action: string;
    entity_type: string;
    details: string;
    created_at: string;
}

export const Dashboard: React.FC = () => {
  const { selectedCompanyId } = useStore();
  const [activities, setActivities] = useState<ActivityLog[]>([]);

  const fetchActivities = useCallback(async () => {
    if (!selectedCompanyId) return;
    try {
      const res = await axios.get(`/api/activities?company_id=${selectedCompanyId}`);
      setActivities(res.data || []);
    } catch (e) {
      console.error(e);
    }
  }, [selectedCompanyId]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    fetchActivities();
    const interval = setInterval(fetchActivities, 10000);
    return () => clearInterval(interval);
  }, [fetchActivities]);

  const formatAction = (log: ActivityLog) => {
      const actionMap: Record<string, string> = {
          'task_created': 'created a new task',
          'task_status_updated': 'updated task status',
          'company_created': 'created workspace',
          'project_created': 'created a new project',
          'agent_created': 'hired a new agent',
          'skill_created': 'added a new skill',
          'skill_file_updated': 'updated skill file'
      };

      const text = actionMap[log.action] || log.action;
      let extra = '';
      if (log.details) {
          try {
              const details = JSON.parse(log.details);
              if (details.status) extra = ` to ${details.status}`;
              if (details.file) extra = ` (${details.file})`;
          } catch { /* ignore */ }
      }
      return `${text}${extra}`;
  };

  return (
    <div className="max-w-6xl mx-auto space-y-6">
      <h1 className="text-2xl font-bold text-gray-900">Dashboard</h1>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
          <div className="bg-white p-6 rounded-lg shadow-sm border flex items-center">
              <div className="bg-indigo-100 p-3 rounded-full mr-4 text-indigo-600"><Users size={24} /></div>
              <div><p className="text-sm text-gray-500 font-medium">Agents Active</p><p className="text-2xl font-bold">1</p></div>
          </div>
          <div className="bg-white p-6 rounded-lg shadow-sm border flex items-center">
              <div className="bg-green-100 p-3 rounded-full mr-4 text-green-600"><CheckCircle size={24} /></div>
              <div><p className="text-sm text-gray-500 font-medium">Tasks in Progress</p><p className="text-2xl font-bold">0</p></div>
          </div>
          <div className="bg-white p-6 rounded-lg shadow-sm border flex items-center">
              <div className="bg-blue-100 p-3 rounded-full mr-4 text-blue-600"><Activity size={24} /></div>
              <div><p className="text-sm text-gray-500 font-medium">System Health</p><p className="text-2xl font-bold text-green-600">Good</p></div>
          </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm border">
          <div className="px-6 py-4 border-b">
              <h2 className="text-lg font-semibold text-gray-800">Latest Activity</h2>
          </div>
          <div className="divide-y divide-gray-100 max-h-96 overflow-y-auto">
              {activities.length === 0 ? (
                  <p className="p-6 text-gray-500 italic">No activity yet. Create a task or agent to get started!</p>
              ) : (
                  activities.map(act => (
                      <div key={act.id} className="p-4 flex items-start hover:bg-gray-50">
                          <div className="h-8 w-8 rounded-full bg-gray-200 flex items-center justify-center text-gray-500 mr-4 flex-shrink-0">
                              <Activity size={16} />
                          </div>
                          <div>
                              <p className="text-sm text-gray-800">System <span className="font-medium">{formatAction(act)}</span></p>
                              <p className="text-xs text-gray-500 mt-1">{new Date(act.created_at).toLocaleString()}</p>
                          </div>
                      </div>
                  ))
              )}
          </div>
      </div>
    </div>
  );
};
