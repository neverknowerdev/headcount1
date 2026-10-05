ALTER TABLE `git_hub_webhook_targets` ADD COLUMN `__enum_guard_wake_status` INTEGER NOT NULL DEFAULT 1 CHECK (`wake_status` IN ('pending', 'processing', 'completed'));
