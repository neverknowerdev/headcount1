-- Create "git_hub_webhook_deliveries" table
CREATE TABLE `git_hub_webhook_deliveries` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `delivery_id` text NULL,
  `event` text NULL,
  `status` text NOT NULL DEFAULT 'processing',
  `forwarded_at` datetime NULL,
  `completed_at` datetime NULL,
  `last_error` text NULL,
  `attempt_token` text NULL,
  `lease_expires_at` datetime NULL,
  `attempts` integer NOT NULL DEFAULT 0,
  `created_at` datetime NULL,
  `updated_at` datetime NULL
);
-- Create index "idx_git_hub_webhook_deliveries_attempt_token" to table: "git_hub_webhook_deliveries"
CREATE INDEX `idx_git_hub_webhook_deliveries_attempt_token` ON `git_hub_webhook_deliveries` (`attempt_token`);
-- Create index "idx_git_hub_webhook_deliveries_delivery_id" to table: "git_hub_webhook_deliveries"
CREATE UNIQUE INDEX `idx_git_hub_webhook_deliveries_delivery_id` ON `git_hub_webhook_deliveries` (`delivery_id`);
