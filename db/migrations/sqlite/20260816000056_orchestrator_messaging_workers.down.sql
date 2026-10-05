DROP INDEX IF EXISTS `idx_run_events_reply_unique`;
DROP INDEX IF EXISTS `idx_run_events_dedupe_nonempty`;
DROP INDEX IF EXISTS `idx_run_events_reply_to`;
DROP INDEX IF EXISTS `idx_run_events_target_pending`;

ALTER TABLE `default_model_settings` DROP COLUMN `__enum_guard_purpose`;
ALTER TABLE `default_model_settings` ADD COLUMN `__enum_guard_purpose` INTEGER NOT NULL DEFAULT 1
  CHECK (`purpose` IN ('commit_messages', 'ask_artifact', 'task_orchestrator'));

ALTER TABLE `run_events` DROP COLUMN `__enum_guard_event_type`;
ALTER TABLE `run_events` ADD COLUMN `__enum_guard_event_type` INTEGER NOT NULL DEFAULT 1
  CHECK (`event_type` IN ('run_status', 'status_report', 'status_report_request', 'worker_question'));
ALTER TABLE `run_events` DROP COLUMN `reply_to_event_id`;
ALTER TABLE `run_events` DROP COLUMN `target_run_id`;
ALTER TABLE `run_events` DROP COLUMN `source_run_id`;

ALTER TABLE `runs` DROP COLUMN `__enum_guard_kind`;
ALTER TABLE `runs` DROP COLUMN `kind`;
ALTER TABLE `agents` DROP COLUMN `can_use_workers`;
