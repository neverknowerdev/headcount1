ALTER TABLE `git_hub_webhook_deliveries` ADD COLUMN `__enum_guard_status` INTEGER NOT NULL DEFAULT 1 CHECK (`status` IN ('pending', 'processing', 'failed', 'completed'));
