-- Irreversible once any task is failed or canceled: those rows cannot be made
-- valid for the older status domain without rewriting user-visible outcomes.
-- Automatic rollback must refuse this migration when such rows exist.
ALTER TABLE `runs` DROP COLUMN `attempt`;
ALTER TABLE `runs` DROP COLUMN `report`;
ALTER TABLE `comments` DROP COLUMN `reply_to_id`;
DROP INDEX IF EXISTS `idx_tasks_origin_step_id`;
DROP INDEX IF EXISTS `idx_tasks_root_task_id`;
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_status`;
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_status` integer NOT NULL DEFAULT 1
  CHECK (`status` IN ('backlog', 'to-do', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'refinement', 'done'));
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_result_verdict`;
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_result_reason`;
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_waiting_on`;
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_phase`;
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_mode`;
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_task_type`;
ALTER TABLE `tasks` DROP COLUMN `design`;
ALTER TABLE `tasks` DROP COLUMN `workspace_owner_task_id`;
ALTER TABLE `tasks` DROP COLUMN `review_round`;
ALTER TABLE `tasks` DROP COLUMN `attempts_used`;
ALTER TABLE `tasks` DROP COLUMN `adjust_cycles_used`;
ALTER TABLE `tasks` DROP COLUMN `smart_steps_used`;
ALTER TABLE `tasks` DROP COLUMN `result_verdict`;
ALTER TABLE `tasks` DROP COLUMN `result_evidence`;
ALTER TABLE `tasks` DROP COLUMN `result_details`;
ALTER TABLE `tasks` DROP COLUMN `result_summary`;
ALTER TABLE `tasks` DROP COLUMN `result_reason`;
ALTER TABLE `tasks` DROP COLUMN `model`;
ALTER TABLE `tasks` DROP COLUMN `model_group_id`;
ALTER TABLE `tasks` DROP COLUMN `provider_id`;
ALTER TABLE `tasks` DROP COLUMN `workflow_phase`;
ALTER TABLE `tasks` DROP COLUMN `origin_phase`;
ALTER TABLE `tasks` DROP COLUMN `origin_step_id`;
ALTER TABLE `tasks` DROP COLUMN `depth`;
ALTER TABLE `tasks` DROP COLUMN `root_task_id`;
ALTER TABLE `tasks` DROP COLUMN `lease_until`;
ALTER TABLE `tasks` DROP COLUMN `lease_owner`;
ALTER TABLE `tasks` DROP COLUMN `wait_detail`;
ALTER TABLE `tasks` DROP COLUMN `wait_until`;
ALTER TABLE `tasks` DROP COLUMN `wait_ref`;
ALTER TABLE `tasks` DROP COLUMN `waiting_on`;
ALTER TABLE `tasks` DROP COLUMN `phase`;
ALTER TABLE `tasks` DROP COLUMN `mode`;
ALTER TABLE `tasks` DROP COLUMN `task_type`;
