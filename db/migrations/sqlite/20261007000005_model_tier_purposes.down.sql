-- The DELETE is irreversible: a user's tier model choices cannot be
-- reconstructed without a backup. This restores the schema domain only.
DELETE FROM `default_model_settings` WHERE `purpose` IN ('smart', 'cheap', 'classifier');
ALTER TABLE `default_model_settings` DROP COLUMN `__enum_guard_purpose`;
ALTER TABLE `default_model_settings` ADD COLUMN `__enum_guard_purpose` integer NOT NULL DEFAULT 1
  CHECK (`purpose` IN ('commit_messages', 'task_orchestrator', 'helper_worker'));
