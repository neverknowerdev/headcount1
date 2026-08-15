-- Create "git_hub_o_auth_states" table
CREATE TABLE `git_hub_o_auth_states` (
  `id` text NULL,
  `redirect_url` text NULL,
  `mcp_server_id` integer NULL DEFAULT 0,
  `user_id` integer NULL DEFAULT 0,
  `mcp_account_id` integer NULL DEFAULT 0,
  `return_path` text NULL,
  `expires_at` datetime NULL,
  `used_at` datetime NULL,
  `created_at` datetime NULL,
  PRIMARY KEY (`id`)
);
