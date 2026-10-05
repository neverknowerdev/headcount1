-- Create "agent_mcp_tool_filters" table
CREATE TABLE `agent_mcp_tool_filters` (
  `agent_id` integer NULL,
  `mcp_server_id` integer NULL,
  `tool_name` text NULL,
  `enabled` numeric NOT NULL DEFAULT true,
  PRIMARY KEY (`agent_id`, `mcp_server_id`, `tool_name`)
);
