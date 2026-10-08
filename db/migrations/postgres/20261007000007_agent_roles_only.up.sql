-- Agents become roles: a name, a description and a prompt. The model an
-- agent ran on now belongs to the task (or its tier), and executors are no
-- longer restricted per agent, so the tool, worker and MCP settings go.
DROP TABLE IF EXISTS "public"."agent_mcp_tool_filters";
DROP TABLE IF EXISTS "public"."agent_mcp_accounts";
DROP TABLE IF EXISTS "public"."agent_mcp_servers";

ALTER TABLE "public"."agents" DROP CONSTRAINT IF EXISTS "ck_agents_chat_type_enum";
ALTER TABLE "public"."agents" DROP CONSTRAINT IF EXISTS "ck_agents_reasoning_level_enum";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "permissions";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "worker_permissions";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "worker_allowed_mc_ps";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "allowed_mc_ps";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "can_use_workers";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "chat_type";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "reasoning_level";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "model";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "provider_id";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "model_group_id";

-- The built-in roles' prompts described the old orchestration and tools that
-- no longer exist. They are reset to the role line; prompts the user wrote
-- for their own agents are kept.
UPDATE "public"."agents" SET "system_prompt" = 'You are the ' || "name" || ' agent.' WHERE "builtin" = true;
