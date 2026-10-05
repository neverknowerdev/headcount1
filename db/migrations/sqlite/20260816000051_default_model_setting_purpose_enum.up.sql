ALTER TABLE `default_model_settings` ADD COLUMN `__enum_guard_purpose` INTEGER NOT NULL DEFAULT 1 CHECK (`purpose` IN ('commit_messages', 'ask_artifact', 'task_orchestrator'));
