-- Model tiers: smart (decides), cheap (executes) and classifier (gates). The
-- orchestrator-era purposes stay valid until the execution clean slate.
ALTER TABLE `default_model_settings` DROP COLUMN `__enum_guard_purpose`;
ALTER TABLE `default_model_settings` ADD COLUMN `__enum_guard_purpose` integer NOT NULL DEFAULT 1
  CHECK (`purpose` IN ('commit_messages', 'task_orchestrator', 'helper_worker', 'smart', 'cheap', 'classifier'));
