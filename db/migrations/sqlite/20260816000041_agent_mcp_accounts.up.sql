-- Create "agent_mcp_accounts" table
CREATE TABLE `agent_mcp_accounts` (
  `agent_id` integer NULL,
  `mcp_account_id` integer NULL,
  `enabled` numeric NOT NULL DEFAULT true,
  PRIMARY KEY (`agent_id`, `mcp_account_id`)
);
