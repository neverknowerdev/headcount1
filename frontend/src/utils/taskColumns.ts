export type TaskColumn = 'status' | 'assignee' | 'project' | 'sprint' | 'priority' | 'relations' | 'taskId' | 'updated' | 'dueDate';

export const COLUMN_LABELS: Record<TaskColumn, string> = {
  status: 'Status',
  assignee: 'Assignee',
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
  'project',
  'sprint',
  'priority',
  'relations',
  'taskId',
  'updated',
  'dueDate',
];
