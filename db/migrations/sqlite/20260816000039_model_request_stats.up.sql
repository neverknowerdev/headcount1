-- Create "model_request_stats" table
CREATE TABLE `model_request_stats` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `group_id` integer NULL,
  `provider_id` integer NOT NULL,
  `model` text NOT NULL,
  `success` numeric NOT NULL DEFAULT false,
  `rate_limited` numeric NOT NULL DEFAULT false,
  `status_code` integer NULL,
  `duration_ms` integer NULL,
  `prompt_tokens` integer NULL,
  `completion_tokens` integer NULL,
  `tokens_per_sec` real NULL,
  `cooldown_until` datetime NULL,
  `error_message` text NULL,
  `created_at` datetime NULL
);
-- Create index "idx_model_request_stats_created_at" to table: "model_request_stats"
CREATE INDEX `idx_model_request_stats_created_at` ON `model_request_stats` (`created_at`);
-- Create index "idx_model_request_stats_model" to table: "model_request_stats"
CREATE INDEX `idx_model_request_stats_model` ON `model_request_stats` (`model`);
-- Create index "idx_model_request_stats_provider_id" to table: "model_request_stats"
CREATE INDEX `idx_model_request_stats_provider_id` ON `model_request_stats` (`provider_id`);
-- Create index "idx_model_request_stats_group_id" to table: "model_request_stats"
CREATE INDEX `idx_model_request_stats_group_id` ON `model_request_stats` (`group_id`);
