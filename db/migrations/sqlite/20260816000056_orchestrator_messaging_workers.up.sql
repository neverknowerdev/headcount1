ALTER TABLE `agents` ADD COLUMN `can_use_workers` integer NOT NULL DEFAULT 0;
UPDATE `agents` SET `can_use_workers` = 1 WHERE lower(trim(`role_key`)) IN ('ceo', 'cto', 'cmo');

ALTER TABLE `runs` ADD COLUMN `kind` text NOT NULL DEFAULT 'agent_session';
UPDATE `runs` SET `kind` = 'task_orchestrator'
WHERE `id` IN (SELECT `orchestrator_run_id` FROM `tasks` WHERE `orchestrator_run_id` IS NOT NULL);
ALTER TABLE `runs` ADD COLUMN `__enum_guard_kind` integer NOT NULL DEFAULT 1
  CHECK (`kind` IN ('task_orchestrator', 'agent_session', 'ceo_consultation', 'helper_worker'));

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

ALTER TABLE `default_model_settings` DROP COLUMN `__enum_guard_purpose`;
ALTER TABLE `default_model_settings` ADD COLUMN `__enum_guard_purpose` INTEGER NOT NULL DEFAULT 1
  CHECK (`purpose` IN ('commit_messages', 'ask_artifact', 'task_orchestrator', 'helper_worker'));
