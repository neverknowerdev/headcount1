ALTER TABLE `tasks` ADD COLUMN `__enum_guard_task_type` INTEGER NOT NULL DEFAULT 1 CHECK (`task_type` IN ('plan and implement', 'implement'));
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_status` INTEGER NOT NULL DEFAULT 1 CHECK (`status` IN ('backlog', 'to-do', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'done'));
