-- Create "run_events" table
CREATE TABLE `run_events` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `task_id` integer NOT NULL,
  `run_id` integer NOT NULL,
  `event_type` text NOT NULL,
  `payload` text NULL,
  `dedupe_key` text NULL,
  `created_at` datetime NULL,
  `consumed_at` datetime NULL
);
-- Create index "idx_run_events_consumed_at" to table: "run_events"
CREATE INDEX `idx_run_events_consumed_at` ON `run_events` (`consumed_at`);
-- Create index "idx_run_events_created_at" to table: "run_events"
CREATE INDEX `idx_run_events_created_at` ON `run_events` (`created_at`);
-- Create index "idx_run_events_dedupe_key" to table: "run_events"
CREATE INDEX `idx_run_events_dedupe_key` ON `run_events` (`dedupe_key`);
-- Create index "idx_run_events_run_id" to table: "run_events"
CREATE INDEX `idx_run_events_run_id` ON `run_events` (`run_id`);
-- Create index "idx_run_events_task_id" to table: "run_events"
CREATE INDEX `idx_run_events_task_id` ON `run_events` (`task_id`);
