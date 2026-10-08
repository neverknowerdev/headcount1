-- Execution clean slate. The orchestrator-and-workers engine is gone; this
-- removes its runs, its messaging tables and its columns. Companies, projects,
-- tasks, agents, providers, model groups and artifacts are kept.

-- Artifacts are deliverables and must outlive the runs that wrote them, so
-- their link to a run becomes optional before any run is deleted. SQLite
-- cannot change a column or a foreign key in place; nothing references
-- artifacts, so the table is rebuilt.
CREATE TABLE `artifacts_new` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NULL,
  `project_id` integer NULL,
  `task_id` integer NOT NULL,
  `run_id` integer NULL,
  `filename` text NOT NULL,
  `file_path` text NOT NULL,
  `content` text NULL,
  `description` text NULL DEFAULT '',
  `is_verified` numeric NOT NULL DEFAULT false,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_artifacts_project` FOREIGN KEY (`project_id`) REFERENCES `projects` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_artifacts_company` FOREIGN KEY (`company_id`) REFERENCES `companies` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_artifacts_run` FOREIGN KEY (`run_id`) REFERENCES `runs` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `fk_artifacts_task` FOREIGN KEY (`task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
INSERT INTO `artifacts_new` (`id`, `company_id`, `project_id`, `task_id`, `run_id`, `filename`, `file_path`, `content`, `description`, `is_verified`, `created_at`, `updated_at`)
SELECT `id`, `company_id`, `project_id`, `task_id`, `run_id`, `filename`, `file_path`, `content`, `description`, `is_verified`, `created_at`, `updated_at` FROM `artifacts`;
DROP TABLE `artifacts`;
ALTER TABLE `artifacts_new` RENAME TO `artifacts`;
CREATE INDEX `idx_artifacts_project_id` ON `artifacts` (`project_id`);
CREATE INDEX `idx_artifacts_company_id` ON `artifacts` (`company_id`);

-- Old runs and everything that only described them.
UPDATE `comments` SET `run_id` = NULL WHERE `run_id` IS NOT NULL;
DROP TABLE `run_status_reports`;
DROP TABLE `run_events`;
DROP TABLE `proxy_request_logs`;
DELETE FROM `runs`;

-- A run is now one executor session: no kinds, no tree of sessions, no
-- self-reported status line.
DROP INDEX IF EXISTS `idx_runs_root_run_id`;
DROP INDEX IF EXISTS `idx_runs_parent_run_id`;
ALTER TABLE `runs` DROP COLUMN `__enum_guard_kind`;
ALTER TABLE `runs` DROP COLUMN `kind`;
ALTER TABLE `runs` DROP COLUMN `parent_run_id`;
ALTER TABLE `runs` DROP COLUMN `root_run_id`;
ALTER TABLE `runs` DROP COLUMN `current_status`;
ALTER TABLE `runs` DROP COLUMN `__enum_guard_status`;
ALTER TABLE `runs` ADD COLUMN `__enum_guard_status` integer NOT NULL DEFAULT 1
  CHECK (`status` IN ('running', 'paused', 'resuming', 'completed', 'failed', 'canceled'));

-- Tasks that were mid-run go back to the backlog: their runs are gone.
-- Tasks in to-do, in review or done are left as they are.
UPDATE `tasks`
SET `status` = 'backlog', `phase` = '', `waiting_on` = '', `wait_ref` = NULL, `wait_until` = NULL,
    `wait_detail` = '', `lease_owner` = '', `lease_until` = NULL, `run_id` = NULL
WHERE `status` IN ('in-progress', 'refinement', 'blocked', 'depends-on-task');
UPDATE `tasks` SET `run_id` = NULL WHERE `run_id` IS NOT NULL;
DROP INDEX IF EXISTS `idx_tasks_orchestrator_run_id`;
ALTER TABLE `tasks` DROP COLUMN `orchestrator_run_id`;
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_status`;
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_status` integer NOT NULL DEFAULT 1
  CHECK (`status` IN ('backlog', 'to-do', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'done', 'failed', 'canceled'));

-- The cheap tier takes over the helper-worker model, or failing that the
-- orchestrator's: both were the cheap models of the old design.
UPDATE `default_model_settings` SET `purpose` = 'cheap'
WHERE `purpose` = 'helper_worker' AND (`provider_id` IS NOT NULL OR `model_group_id` IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM `default_model_settings` AS other WHERE other.`user_id` = `default_model_settings`.`user_id` AND other.`purpose` = 'cheap');
UPDATE `default_model_settings` SET `purpose` = 'cheap'
WHERE `purpose` = 'task_orchestrator' AND (`provider_id` IS NOT NULL OR `model_group_id` IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM `default_model_settings` AS other WHERE other.`user_id` = `default_model_settings`.`user_id` AND other.`purpose` = 'cheap');
DELETE FROM `default_model_settings` WHERE `purpose` IN ('task_orchestrator', 'helper_worker');
-- The smart tier takes the model the user's CEO agent ran on: agents were
-- where the strong models were configured.
INSERT INTO `default_model_settings` (`purpose`, `user_id`, `provider_id`, `model`, `model_group_id`, `created_at`, `updated_at`)
SELECT 'smart', company.`user_id`, agent.`provider_id`, COALESCE(agent.`model`, ''), agent.`model_group_id`, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM `agents` AS agent JOIN `companies` AS company ON company.`id` = agent.`company_id`
WHERE agent.`id` IN (
    SELECT MIN(a.`id`) FROM `agents` AS a JOIN `companies` AS co ON co.`id` = a.`company_id`
    WHERE co.`user_id` IS NOT NULL AND lower(trim(a.`role_key`)) = 'ceo'
      AND (a.`provider_id` IS NOT NULL OR a.`model_group_id` IS NOT NULL)
    GROUP BY co.`user_id`)
  AND NOT EXISTS (SELECT 1 FROM `default_model_settings` AS other WHERE other.`user_id` = company.`user_id` AND other.`purpose` = 'smart');
ALTER TABLE `default_model_settings` DROP COLUMN `__enum_guard_purpose`;
ALTER TABLE `default_model_settings` ADD COLUMN `__enum_guard_purpose` integer NOT NULL DEFAULT 1
  CHECK (`purpose` IN ('commit_messages', 'smart', 'cheap', 'classifier'));
