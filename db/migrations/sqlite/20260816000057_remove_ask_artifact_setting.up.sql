DELETE FROM `default_model_settings` WHERE `purpose` = 'ask_artifact';
ALTER TABLE `default_model_settings` DROP COLUMN `__enum_guard_purpose`;
ALTER TABLE `default_model_settings` ADD COLUMN `__enum_guard_purpose` INTEGER NOT NULL DEFAULT 1
  CHECK (`purpose` IN ('commit_messages', 'task_orchestrator', 'helper_worker'));
