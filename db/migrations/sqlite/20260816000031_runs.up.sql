-- Create "runs" table
CREATE TABLE `runs` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `task_id` integer NOT NULL,
  `agent_id` integer NOT NULL,
  `name` text NULL,
  `parent_run_id` integer NULL,
  `root_run_id` integer NULL,
  `current_status` text NULL DEFAULT '',
  `status` text NOT NULL,
  `session_id` text NULL,
  `log_file_path` text NULL,
  `log_content` text NULL,
  `log_entries` text NULL,
  `token_stats` text NULL,
  `result_description` text NULL,
  `result_explanation` text NULL,
  `started_at` datetime NULL,
  `ended_at` datetime NULL,
  `last_message_time` datetime NULL,
  `recovery` jsonb NULL,
  CONSTRAINT `fk_runs_agent` FOREIGN KEY (`agent_id`) REFERENCES `agents` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_runs_task` FOREIGN KEY (`task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_runs_root_run_id" to table: "runs"
CREATE INDEX `idx_runs_root_run_id` ON `runs` (`root_run_id`);
-- Create index "idx_runs_parent_run_id" to table: "runs"
CREATE INDEX `idx_runs_parent_run_id` ON `runs` (`parent_run_id`);
-- Create index "idx_runs_name" to table: "runs"
CREATE INDEX `idx_runs_name` ON `runs` (`name`);
