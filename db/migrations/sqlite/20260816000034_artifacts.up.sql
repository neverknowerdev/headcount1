-- Create "artifacts" table
CREATE TABLE `artifacts` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NULL,
  `project_id` integer NULL,
  `task_id` integer NOT NULL,
  `run_id` integer NOT NULL,
  `filename` text NOT NULL,
  `file_path` text NOT NULL,
  `content` text NULL,
  `description` text NULL DEFAULT '',
  `is_verified` numeric NOT NULL DEFAULT false,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_artifacts_project` FOREIGN KEY (`project_id`) REFERENCES `projects` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_artifacts_company` FOREIGN KEY (`company_id`) REFERENCES `companies` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_artifacts_run` FOREIGN KEY (`run_id`) REFERENCES `runs` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_artifacts_task` FOREIGN KEY (`task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_artifacts_project_id" to table: "artifacts"
CREATE INDEX `idx_artifacts_project_id` ON `artifacts` (`project_id`);
-- Create index "idx_artifacts_company_id" to table: "artifacts"
CREATE INDEX `idx_artifacts_company_id` ON `artifacts` (`company_id`);
