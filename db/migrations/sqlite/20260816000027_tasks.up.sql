-- Create "tasks" table
CREATE TABLE `tasks` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NOT NULL,
  `project_id` integer NULL,
  `sprint_id` integer NOT NULL,
  `agent_id` integer NULL,
  `parent_id` integer NULL,
  `title` text NOT NULL,
  `task_type` text NOT NULL DEFAULT 'plan and implement',
  `description` text NULL,
  `ref_key` text NULL,
  `refined_description` text NULL DEFAULT '',
  `acceptance_criteria` text NULL DEFAULT '',
  `test_cases` text NULL DEFAULT '',
  `priority` text NOT NULL DEFAULT 'Normal',
  `status` text NOT NULL DEFAULT 'backlog',
  `due_date` datetime NULL,
  `is_archived` numeric NOT NULL DEFAULT false,
  `run_id` integer NULL,
  `orchestrator_run_id` integer NULL,
  `git_hub_pr_number` integer NULL,
  `git_hub_pr_url` text NULL,
  `git_hub_branch` text NULL,
  `git_base_branch` text NOT NULL DEFAULT 'main',
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_tasks_parent` FOREIGN KEY (`parent_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `fk_tasks_agent` FOREIGN KEY (`agent_id`) REFERENCES `agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `fk_tasks_sprint` FOREIGN KEY (`sprint_id`) REFERENCES `sprints` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_tasks_project` FOREIGN KEY (`project_id`) REFERENCES `projects` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `fk_tasks_company` FOREIGN KEY (`company_id`) REFERENCES `companies` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_tasks_git_hub_branch" to table: "tasks"
CREATE INDEX `idx_tasks_git_hub_branch` ON `tasks` (`git_hub_branch`);
-- Create index "idx_tasks_orchestrator_run_id" to table: "tasks"
CREATE INDEX `idx_tasks_orchestrator_run_id` ON `tasks` (`orchestrator_run_id`);
-- Create index "idx_tasks_ref_key" to table: "tasks"
CREATE INDEX `idx_tasks_ref_key` ON `tasks` (`ref_key`);
