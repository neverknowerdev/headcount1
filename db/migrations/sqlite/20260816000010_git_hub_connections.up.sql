-- Create "git_hub_connections" table
CREATE TABLE `git_hub_connections` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `installation_id` integer NULL,
  `mcp_account_id` integer NULL,
  `user_id` integer NULL,
  `account_login` text NULL,
  `connected_at` datetime NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL
);
-- Create index "idx_git_hub_connections_user_id" to table: "git_hub_connections"
CREATE INDEX `idx_git_hub_connections_user_id` ON `git_hub_connections` (`user_id`);
-- Create index "idx_git_hub_connections_mcp_account_id" to table: "git_hub_connections"
CREATE INDEX `idx_git_hub_connections_mcp_account_id` ON `git_hub_connections` (`mcp_account_id`);
-- Create index "idx_git_hub_connections_installation_id" to table: "git_hub_connections"
CREATE INDEX `idx_git_hub_connections_installation_id` ON `git_hub_connections` (`installation_id`);
