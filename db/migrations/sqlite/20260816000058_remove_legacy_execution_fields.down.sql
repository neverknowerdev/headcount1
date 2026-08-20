ALTER TABLE `agents` ADD COLUMN `mode` text NOT NULL DEFAULT 'primary';
ALTER TABLE `agents` ADD COLUMN `subagents` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `task_type` text NOT NULL DEFAULT 'plan and implement';
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_task_type` INTEGER NOT NULL DEFAULT 1 CHECK (`task_type` IN ('plan and implement', 'implement'));
