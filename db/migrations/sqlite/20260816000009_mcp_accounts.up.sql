-- Create "mcp_accounts" table
CREATE TABLE `mcp_accounts` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `mcp_server_id` integer NOT NULL,
  `name` text NOT NULL,
  `auth_token` text NULL,
  `user_id` integer NULL,
  `last_error` text NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_mcp_servers_accounts` FOREIGN KEY (`mcp_server_id`) REFERENCES `mcp_servers` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "idx_mcp_accounts_user_id" to table: "mcp_accounts"
CREATE INDEX `idx_mcp_accounts_user_id` ON `mcp_accounts` (`user_id`);
-- Create index "idx_mcp_accounts_mcp_server_id" to table: "mcp_accounts"
CREATE INDEX `idx_mcp_accounts_mcp_server_id` ON `mcp_accounts` (`mcp_server_id`);
