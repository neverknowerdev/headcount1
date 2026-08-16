-- Create "git_hub_identities" table
CREATE TABLE `git_hub_identities` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `mcp_account_id` integer NOT NULL,
  `mcp_server_id` integer NOT NULL,
  `user_id` integer NOT NULL,
  `git_hub_user_id` integer NOT NULL,
  `git_hub_login` text NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL
);
-- Create index "idx_github_identity" to table: "git_hub_identities"
CREATE UNIQUE INDEX `idx_github_identity` ON `git_hub_identities` (`mcp_server_id`, `user_id`, `git_hub_user_id`);
-- Create index "idx_git_hub_identities_mcp_account_id" to table: "git_hub_identities"
CREATE UNIQUE INDEX `idx_git_hub_identities_mcp_account_id` ON `git_hub_identities` (`mcp_account_id`);
