-- Create "agent_mcp_servers" table
CREATE TABLE `agent_mcp_servers` (
  `agent_id` integer NULL,
  `mcp_server_id` integer NULL,
  `enabled` numeric NOT NULL DEFAULT true,
  PRIMARY KEY (`agent_id`, `mcp_server_id`)
);
