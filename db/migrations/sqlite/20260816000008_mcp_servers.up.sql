-- Create "mcp_servers" table
CREATE TABLE `mcp_servers` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `owner_user_id` integer NULL,
  `display_name` text NULL,
  `description` text NULL,
  `transport` text NOT NULL,
  `command` text NULL,
  `args` text NULL,
  `url` text NULL,
  `headers` text NULL,
  `auth_type` text NULL,
  `auth_env_var` text NULL,
  `tools_cache` text NULL,
  `last_error` text NULL,
  `init_status` text NULL DEFAULT '',
  `deps` text NULL,
  `enabled` numeric NOT NULL DEFAULT true,
  `builtin` numeric NOT NULL DEFAULT false,
  `work_dir` text NULL,
  `project_id` integer NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_mcp_servers_project` FOREIGN KEY (`project_id`) REFERENCES `projects` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_mcp_servers_project_id" to table: "mcp_servers"
CREATE INDEX `idx_mcp_servers_project_id` ON `mcp_servers` (`project_id`);
-- Create index "idx_mcp_servers_owner_user_id" to table: "mcp_servers"
CREATE INDEX `idx_mcp_servers_owner_user_id` ON `mcp_servers` (`owner_user_id`);
-- Create index "idx_mcp_servers_name" to table: "mcp_servers"
CREATE UNIQUE INDEX `idx_mcp_servers_name` ON `mcp_servers` (`name`);
