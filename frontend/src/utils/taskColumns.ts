export type TaskColumn = 'status' | 'assignee' | 'agent' | 'project' | 'sprint' | 'priority' | 'relations' | 'taskId' | 'updated' | 'dueDate';

export const COLUMN_LABELS: Record<TaskColumn, string> = {
  status: 'Status',
  assignee: 'Assignee',
  agent: 'Agent',
  project: 'Project',
  sprint: 'Sprint',
  priority: 'Priority',
  relations: 'Relations',
  taskId: 'Task ID',
  updated: 'Updated',
  dueDate: 'Due date',
};

export const DISPLAY_ORDER: TaskColumn[] = [
  'status',
  'assignee',
  'agent',
  'project',
  'sprint',
  'priority',
  'relations',
  'taskId',
  'updated',
  'dueDate',
];
