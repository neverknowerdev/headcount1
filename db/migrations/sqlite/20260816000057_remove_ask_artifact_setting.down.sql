-- The DELETE in the up migration is irreversible: user-specific model settings
-- cannot be reconstructed without a backup. This restores the schema domain
-- only; automatic deployment rollback must refuse this migration when rows
-- need to be restored.
ALTER TABLE `default_model_settings` DROP COLUMN `__enum_guard_purpose`;
ALTER TABLE `default_model_settings` ADD COLUMN `__enum_guard_purpose` INTEGER NOT NULL DEFAULT 1
  CHECK (`purpose` IN ('commit_messages', 'ask_artifact', 'task_orchestrator', 'helper_worker'));
