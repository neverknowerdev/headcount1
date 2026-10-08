-- Irreversible: the agents' model, tool and MCP settings and the built-in
-- prompts cannot be reconstructed. This restores the legacy schema shape for
-- an operator-assisted rollback only; automatic rollback must refuse this
-- migration. (The legacy foreign keys on provider_id and model_group_id are
-- not restored: SQLite cannot add one to an existing table.)
ALTER TABLE `agents` ADD COLUMN `provider_id` integer NULL;
ALTER TABLE `agents` ADD COLUMN `model_group_id` integer NULL;
ALTER TABLE `agents` ADD COLUMN `model` text NULL;
ALTER TABLE `agents` ADD COLUMN `chat_type` text NOT NULL DEFAULT 'message_history';
ALTER TABLE `agents` ADD COLUMN `reasoning_level` text NULL DEFAULT '';
ALTER TABLE `agents` ADD COLUMN `allowed_mc_ps` text NULL DEFAULT '';
ALTER TABLE `agents` ADD COLUMN `permissions` text NULL;
ALTER TABLE `agents` ADD COLUMN `__enum_guard_chat_type` integer NOT NULL DEFAULT 1 CHECK (`chat_type` IN ('message_history', 'compact_thinking'));
ALTER TABLE `agents` ADD COLUMN `__enum_guard_reasoning_level` integer NOT NULL DEFAULT 1 CHECK (`reasoning_level` IS NULL OR `reasoning_level` IN ('', 'low', 'medium', 'max'));
ALTER TABLE `agents` ADD COLUMN `can_use_workers` integer NOT NULL DEFAULT 0;
ALTER TABLE `agents` ADD COLUMN `worker_permissions` text NOT NULL DEFAULT '';
ALTER TABLE `agents` ADD COLUMN `worker_allowed_mc_ps` text NOT NULL DEFAULT '';
-- Create "agent_mcp_servers" table
CREATE TABLE `agent_mcp_servers` (
  `agent_id` integer NULL,
  `mcp_server_id` integer NULL,
  `enabled` numeric NOT NULL DEFAULT true,
  PRIMARY KEY (`agent_id`, `mcp_server_id`)
);
-- Create "agent_mcp_accounts" table
CREATE TABLE `agent_mcp_accounts` (
  `agent_id` integer NULL,
  `mcp_account_id` integer NULL,
  `enabled` numeric NOT NULL DEFAULT true,
  PRIMARY KEY (`agent_id`, `mcp_account_id`)
);
-- Create "agent_mcp_tool_filters" table
CREATE TABLE `agent_mcp_tool_filters` (
  `agent_id` integer NULL,
  `mcp_server_id` integer NULL,
  `tool_name` text NULL,
  `enabled` numeric NOT NULL DEFAULT true,
  PRIMARY KEY (`agent_id`, `mcp_server_id`, `tool_name`)
);
