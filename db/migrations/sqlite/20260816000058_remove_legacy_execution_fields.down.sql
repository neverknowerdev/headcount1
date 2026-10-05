-- The dropped columns' row values cannot be reconstructed after the up
-- migration. This irreversible rollback restores the legacy schema shape for an operator-assisted
-- rollback only; automatic rollback must refuse this migration.
ALTER TABLE `agents` ADD COLUMN `mode` text NOT NULL DEFAULT 'primary';
ALTER TABLE `agents` ADD COLUMN `subagents` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `task_type` text NOT NULL DEFAULT 'plan and implement';
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_task_type` INTEGER NOT NULL DEFAULT 1 CHECK (`task_type` IN ('plan and implement', 'implement'));
