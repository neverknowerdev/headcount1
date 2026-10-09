-- Irreversible: the deleted runs, their messages and the model choices that
-- were folded into the tiers cannot be reconstructed. This restores the
-- legacy schema shape for an operator-assisted rollback only; automatic
-- rollback must refuse this migration.
ALTER TABLE `default_model_settings` DROP COLUMN `__enum_guard_purpose`;
ALTER TABLE `default_model_settings` ADD COLUMN `__enum_guard_purpose` integer NOT NULL DEFAULT 1
  CHECK (`purpose` IN ('commit_messages', 'task_orchestrator', 'helper_worker', 'smart', 'cheap', 'classifier'));
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_status`;
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_status` integer NOT NULL DEFAULT 1
  CHECK (`status` IN ('backlog', 'to-do', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'refinement', 'done', 'failed', 'canceled'));
ALTER TABLE `tasks` ADD COLUMN `orchestrator_run_id` integer NULL;
CREATE INDEX `idx_tasks_orchestrator_run_id` ON `tasks` (`orchestrator_run_id`);
ALTER TABLE `runs` DROP COLUMN `__enum_guard_status`;
ALTER TABLE `runs` ADD COLUMN `__enum_guard_status` integer NOT NULL DEFAULT 1
  CHECK (`status` IN ('running', 'completed', 'failed', 'canceled', 'paused', 'recoverable_failed', 'stale', 'resuming', 'waiting', 'interrupted'));
ALTER TABLE `runs` ADD COLUMN `current_status` text NULL DEFAULT '';
ALTER TABLE `runs` ADD COLUMN `root_run_id` integer NULL;
ALTER TABLE `runs` ADD COLUMN `parent_run_id` integer NULL;
ALTER TABLE `runs` ADD COLUMN `kind` text NOT NULL DEFAULT 'agent_session';
ALTER TABLE `runs` ADD COLUMN `__enum_guard_kind` integer NOT NULL DEFAULT 1
  CHECK (`kind` IN ('task_orchestrator', 'agent_session', 'ceo_consultation', 'helper_worker'));
CREATE INDEX `idx_runs_parent_run_id` ON `runs` (`parent_run_id`);
CREATE INDEX `idx_runs_root_run_id` ON `runs` (`root_run_id`);
-- Create "run_status_reports" table
CREATE TABLE `run_status_reports` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `run_id` integer NOT NULL,
  `status` text NOT NULL,
  `message_id` integer NULL,
  `reported_at` datetime NULL,
  CONSTRAINT `fk_run_status_reports_run` FOREIGN KEY (`run_id`) REFERENCES `runs` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_run_status_reports_reported_at" to table: "run_status_reports"
CREATE INDEX `idx_run_status_reports_reported_at` ON `run_status_reports` (`reported_at`);
-- Create index "idx_run_status_reports_message_id" to table: "run_status_reports"
CREATE INDEX `idx_run_status_reports_message_id` ON `run_status_reports` (`message_id`);
-- Create index "idx_run_status_reports_run_id" to table: "run_status_reports"
CREATE INDEX `idx_run_status_reports_run_id` ON `run_status_reports` (`run_id`);
-- Create "run_events" table
CREATE TABLE `run_events` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `task_id` integer NOT NULL,
  `run_id` integer NOT NULL,
  `event_type` text NOT NULL,
  `payload` text NULL,
  `dedupe_key` text NULL,
  `created_at` datetime NULL,
  `consumed_at` datetime NULL
);
-- Create index "idx_run_events_consumed_at" to table: "run_events"
CREATE INDEX `idx_run_events_consumed_at` ON `run_events` (`consumed_at`);
-- Create index "idx_run_events_created_at" to table: "run_events"
CREATE INDEX `idx_run_events_created_at` ON `run_events` (`created_at`);
-- Create index "idx_run_events_dedupe_key" to table: "run_events"
CREATE INDEX `idx_run_events_dedupe_key` ON `run_events` (`dedupe_key`);
-- Create index "idx_run_events_run_id" to table: "run_events"
CREATE INDEX `idx_run_events_run_id` ON `run_events` (`run_id`);
-- Create index "idx_run_events_task_id" to table: "run_events"
CREATE INDEX `idx_run_events_task_id` ON `run_events` (`task_id`);
ALTER TABLE `run_events` ADD COLUMN `__enum_guard_event_type` INTEGER NOT NULL DEFAULT 1 CHECK (`event_type` IN ('run_status', 'status_report', 'status_report_request', 'worker_question'));
ALTER TABLE `run_events` ADD COLUMN `source_run_id` integer;
ALTER TABLE `run_events` ADD COLUMN `target_run_id` integer;
ALTER TABLE `run_events` ADD COLUMN `reply_to_event_id` integer;
UPDATE `run_events` SET `source_run_id` = `run_id` WHERE `source_run_id` IS NULL;
ALTER TABLE `run_events` DROP COLUMN `__enum_guard_event_type`;
ALTER TABLE `run_events` ADD COLUMN `__enum_guard_event_type` integer NOT NULL DEFAULT 1
  CHECK (`event_type` IN ('run_status', 'status_report', 'status_report_request', 'worker_question', 'session_message', 'session_message_answer', 'worker_finished'));
CREATE INDEX `idx_run_events_target_pending` ON `run_events` (`target_run_id`, `consumed_at`, `created_at`);
CREATE INDEX `idx_run_events_reply_to` ON `run_events` (`reply_to_event_id`);
CREATE UNIQUE INDEX `idx_run_events_dedupe_nonempty` ON `run_events` (`dedupe_key`) WHERE `dedupe_key` <> '';
CREATE UNIQUE INDEX `idx_run_events_reply_unique` ON `run_events` (`reply_to_event_id`) WHERE `reply_to_event_id` IS NOT NULL;
ALTER TABLE `run_events` DROP COLUMN `__enum_guard_event_type`;
ALTER TABLE `run_events` ADD COLUMN `__enum_guard_event_type` integer NOT NULL DEFAULT 1
  CHECK (`event_type` IN ('run_status', 'status_report', 'status_report_request', 'worker_question', 'session_message', 'session_message_answer', 'worker_finished', 'human_input_requested', 'human_input_answered'));
-- Create "proxy_request_logs" table
CREATE TABLE `proxy_request_logs` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `agent_id` integer NOT NULL,
  `provider_id` integer NOT NULL,
  `model` text NOT NULL,
  `prompt_tokens` integer NULL,
  `completion_tokens` integer NULL,
  `total_tokens` integer NULL,
  `created_at` datetime NULL,
  CONSTRAINT `fk_proxy_request_logs_provider` FOREIGN KEY (`provider_id`) REFERENCES `llm_providers` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_proxy_request_logs_agent` FOREIGN KEY (`agent_id`) REFERENCES `agents` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
