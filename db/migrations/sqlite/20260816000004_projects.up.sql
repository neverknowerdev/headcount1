-- Create "projects" table
CREATE TABLE `projects` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NOT NULL,
  `name` text NOT NULL,
  `description` text NULL,
  `workspace_folder` text NULL,
  `repository_url` text NULL,
  `git_hub_repository_id` integer NULL,
  `git_hub_installation_id` integer NULL,
  `git_hub_default_branch` text NULL,
  `is_external` numeric NOT NULL DEFAULT false,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_projects_company` FOREIGN KEY (`company_id`) REFERENCES `companies` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_projects_git_hub_installation_id" to table: "projects"
CREATE INDEX `idx_projects_git_hub_installation_id` ON `projects` (`git_hub_installation_id`);
-- Create index "idx_projects_git_hub_repository_id" to table: "projects"
CREATE INDEX `idx_projects_git_hub_repository_id` ON `projects` (`git_hub_repository_id`);
