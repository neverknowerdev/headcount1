-- Create "git_hub_webhook_targets" table
CREATE TABLE `git_hub_webhook_targets` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `delivery_id` text NOT NULL,
  `task_id` integer NOT NULL,
  `comment_id` integer NOT NULL,
  `wake_status` text NOT NULL DEFAULT 'pending',
  `wake_attempt_token` text NULL,
  `wake_lease_expires_at` datetime NULL,
  `wake_attempts` integer NOT NULL DEFAULT 0,
  `wake_last_error` text NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL
);
-- Create index "idx_git_hub_webhook_targets_wake_attempt_token" to table: "git_hub_webhook_targets"
CREATE INDEX `idx_git_hub_webhook_targets_wake_attempt_token` ON `git_hub_webhook_targets` (`wake_attempt_token`);
-- Create index "idx_github_delivery_task" to table: "git_hub_webhook_targets"
CREATE UNIQUE INDEX `idx_github_delivery_task` ON `git_hub_webhook_targets` (`delivery_id`, `task_id`);
