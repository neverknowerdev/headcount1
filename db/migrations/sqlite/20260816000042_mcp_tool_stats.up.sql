-- Create "mcp_tool_stats" table
CREATE TABLE `mcp_tool_stats` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `mcp_server_id` integer NOT NULL,
  `tool_name` text NOT NULL,
  `call_count` integer NOT NULL DEFAULT 0
);
-- Create index "idx_mcp_tool_stat" to table: "mcp_tool_stats"
CREATE UNIQUE INDEX `idx_mcp_tool_stat` ON `mcp_tool_stats` (`mcp_server_id`, `tool_name`);
