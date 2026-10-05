-- Create "run_status_reports" table
CREATE TABLE `run_status_reports` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `run_id` integer NOT NULL,
  `status` text NOT NULL,
  `message_id` integer NULL,
  `reported_at` datetime NULL,
  CONSTRAINT `fk_run_status_reports_run` FOREIGN KEY (`run_id`) REFERENCES `runs` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_run_status_reports_reported_at" to table: "run_status_reports"
CREATE INDEX `idx_run_status_reports_reported_at` ON `run_status_reports` (`reported_at`);
-- Create index "idx_run_status_reports_message_id" to table: "run_status_reports"
CREATE INDEX `idx_run_status_reports_message_id` ON `run_status_reports` (`message_id`);
-- Create index "idx_run_status_reports_run_id" to table: "run_status_reports"
CREATE INDEX `idx_run_status_reports_run_id` ON `run_status_reports` (`run_id`);
