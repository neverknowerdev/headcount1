-- Workflow state on tasks: type, mode, phase, a typed wait, a driver lease,
-- tree position, a per-task model override, a structured result and budgets.
-- task_type reuses the column name dropped by 20260816000058 with a new domain.
-- provider_id/model_group_id deliberately carry no foreign key: SQLite cannot
-- drop a column that is part of one, and a deleted target simply means the
-- task falls back to its tier default.
ALTER TABLE `tasks` ADD COLUMN `task_type` text NOT NULL DEFAULT 'general';
ALTER TABLE `tasks` ADD COLUMN `mode` text NOT NULL DEFAULT 'managed';
ALTER TABLE `tasks` ADD COLUMN `phase` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `waiting_on` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `wait_ref` integer NULL;
ALTER TABLE `tasks` ADD COLUMN `wait_until` datetime NULL;
ALTER TABLE `tasks` ADD COLUMN `wait_detail` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `lease_owner` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `lease_until` datetime NULL;
ALTER TABLE `tasks` ADD COLUMN `root_task_id` integer NOT NULL DEFAULT 0;
ALTER TABLE `tasks` ADD COLUMN `depth` integer NOT NULL DEFAULT 0;
ALTER TABLE `tasks` ADD COLUMN `origin_step_id` integer NULL;
ALTER TABLE `tasks` ADD COLUMN `origin_phase` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `workflow_phase` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `provider_id` integer NULL;
ALTER TABLE `tasks` ADD COLUMN `model_group_id` integer NULL;
ALTER TABLE `tasks` ADD COLUMN `model` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `result_reason` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `result_summary` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `result_details` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `result_evidence` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `result_verdict` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `smart_steps_used` integer NOT NULL DEFAULT 0;
ALTER TABLE `tasks` ADD COLUMN `adjust_cycles_used` integer NOT NULL DEFAULT 0;
ALTER TABLE `tasks` ADD COLUMN `attempts_used` integer NOT NULL DEFAULT 0;
ALTER TABLE `tasks` ADD COLUMN `review_round` integer NOT NULL DEFAULT 0;
ALTER TABLE `tasks` ADD COLUMN `workspace_owner_task_id` integer NULL;
ALTER TABLE `tasks` ADD COLUMN `design` text NOT NULL DEFAULT '';
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_task_type` integer NOT NULL DEFAULT 1
  CHECK (`task_type` IN ('research', 'coding', 'review', 'general'));
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_mode` integer NOT NULL DEFAULT 1
  CHECK (`mode` IN ('managed', 'direct'));
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_phase` integer NOT NULL DEFAULT 1
  CHECK (`phase` IN ('', 'refine', 'design', 'test_plan', 'plan', 'execute', 'adjust', 'verify'));
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_waiting_on` integer NOT NULL DEFAULT 1
  CHECK (`waiting_on` IN ('', 'subtasks', 'run', 'human', 'vault', 'config', 'workspace', 'backoff', 'operator'));
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_result_reason` integer NOT NULL DEFAULT 1
  CHECK (`result_reason` IN ('', 'reported_failure', 'cannot_complete', 'run_error', 'budget_exhausted', 'prerequisite_failed', 'stopped'));
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_result_verdict` integer NOT NULL DEFAULT 1
  CHECK (`result_verdict` IN ('', 'approved', 'changes_requested'));

-- failed and canceled are terminal outcomes of subtasks; done keeps meaning succeeded.
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_status`;
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_status` integer NOT NULL DEFAULT 1
  CHECK (`status` IN ('backlog', 'to-do', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'refinement', 'done', 'failed', 'canceled'));

-- Backfill the tree position of existing tasks.
WITH RECURSIVE `tree`(`id`, `root_id`, `depth`) AS (
  SELECT `id`, `id`, 0 FROM `tasks` WHERE `parent_id` IS NULL
  UNION ALL
  SELECT `child`.`id`, `tree`.`root_id`, `tree`.`depth` + 1
  FROM `tasks` AS `child` JOIN `tree` ON `child`.`parent_id` = `tree`.`id`
)
UPDATE `tasks` SET
  `root_task_id` = COALESCE((SELECT `root_id` FROM `tree` WHERE `tree`.`id` = `tasks`.`id`), `tasks`.`id`),
  `depth` = COALESCE((SELECT `depth` FROM `tree` WHERE `tree`.`id` = `tasks`.`id`), 0);
CREATE INDEX `idx_tasks_root_task_id` ON `tasks` (`root_task_id`);
CREATE INDEX `idx_tasks_origin_step_id` ON `tasks` (`origin_step_id`);

-- A human answer names the question it replies to.
ALTER TABLE `comments` ADD COLUMN `reply_to_id` integer NULL;

-- An executor session stores its structured report on its own row; the task's
-- workflow step then applies it. attempt numbers the retries of one task.
ALTER TABLE `runs` ADD COLUMN `report` text NOT NULL DEFAULT '';
ALTER TABLE `runs` ADD COLUMN `attempt` integer NOT NULL DEFAULT 0;
