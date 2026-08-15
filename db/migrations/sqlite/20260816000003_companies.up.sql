-- Create "companies" table
CREATE TABLE `companies` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `short_name` text NOT NULL,
  `description` text NULL,
  `color` text NULL,
  `team_id` integer NULL,
  `user_id` integer NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL
);
-- Create index "idx_companies_user_id" to table: "companies"
CREATE INDEX `idx_companies_user_id` ON `companies` (`user_id`);
-- Create index "idx_companies_team_id" to table: "companies"
CREATE INDEX `idx_companies_team_id` ON `companies` (`team_id`);
