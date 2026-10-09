-- Agents become roles: a name, a description and a prompt. The model an
-- agent ran on now belongs to the task (or its tier), and executors are no
-- longer restricted per agent, so the tool, worker and MCP settings go.
DROP TABLE `agent_mcp_tool_filters`;
DROP TABLE `agent_mcp_accounts`;
DROP TABLE `agent_mcp_servers`;

-- provider_id and model_group_id are part of foreign keys, and SQLite cannot
-- drop such a column, so the table is rebuilt. Dropping the old table fires
-- the foreign-key actions of the rows that point at it: task assignments
-- would be cleared and agent-skill links would block the drop. Both are set
-- aside first and put back afterwards. (Runs also point at agents; the
-- clean-slate migration has just emptied that table.)
CREATE TABLE `_agent_roles_task_agents` AS SELECT `id`, `agent_id` FROM `tasks` WHERE `agent_id` IS NOT NULL;
CREATE TABLE `_agent_roles_skills` AS SELECT `agent_id`, `skill_id` FROM `agent_skills`;
DELETE FROM `agent_skills`;
CREATE TABLE `agents_new` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NOT NULL,
  `name` text NOT NULL,
  `role_key` text NULL DEFAULT '',
  `short_name` text NULL DEFAULT '',
  `description` text NULL,
  `system_prompt` text NOT NULL,
  `builtin` numeric NOT NULL DEFAULT false,
  `enabled` numeric NOT NULL DEFAULT true,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_agents_company` FOREIGN KEY (`company_id`) REFERENCES `companies` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
INSERT INTO `agents_new` (`id`, `company_id`, `name`, `role_key`, `short_name`, `description`, `system_prompt`, `builtin`, `enabled`, `created_at`, `updated_at`)
SELECT `id`, `company_id`, `name`, `role_key`, `short_name`, `description`, `system_prompt`, `builtin`, `enabled`, `created_at`, `updated_at` FROM `agents`;
DROP TABLE `agents`;
ALTER TABLE `agents_new` RENAME TO `agents`;
CREATE INDEX `idx_agents_role_key` ON `agents` (`role_key`);
UPDATE `tasks` SET `agent_id` = (SELECT `agent_id` FROM `_agent_roles_task_agents` AS saved WHERE saved.`id` = `tasks`.`id`)
WHERE `id` IN (SELECT `id` FROM `_agent_roles_task_agents`);
INSERT INTO `agent_skills` (`agent_id`, `skill_id`) SELECT `agent_id`, `skill_id` FROM `_agent_roles_skills`;
DROP TABLE `_agent_roles_task_agents`;
DROP TABLE `_agent_roles_skills`;

-- The built-in roles' prompts described the old orchestration and tools that
-- no longer exist. They are reset to the role line; prompts the user wrote
-- for their own agents are kept.
UPDATE `agents` SET `system_prompt` = 'You are the ' || `name` || ' agent.' WHERE `builtin` = true;
