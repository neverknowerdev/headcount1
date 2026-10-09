-- Irreversible: the agents' model, tool and MCP settings and the built-in
-- prompts cannot be reconstructed. This restores the legacy schema shape for
-- an operator-assisted rollback only; automatic rollback must refuse this
-- migration.
ALTER TABLE "public"."agents" ADD COLUMN "provider_id" integer NULL;
ALTER TABLE "public"."agents" ADD COLUMN "model_group_id" integer NULL;
ALTER TABLE "public"."agents" ADD COLUMN "model" text NULL;
ALTER TABLE "public"."agents" ADD COLUMN "chat_type" text NOT NULL DEFAULT 'message_history';
ALTER TABLE "public"."agents" ADD COLUMN "reasoning_level" text NULL DEFAULT '';
ALTER TABLE "public"."agents" ADD COLUMN "allowed_mc_ps" text NULL DEFAULT '';
ALTER TABLE "public"."agents" ADD COLUMN "permissions" text NULL;
ALTER TABLE "public"."agents" ADD CONSTRAINT "ck_agents_chat_type_enum" CHECK ("chat_type" IN ('message_history', 'compact_thinking'));
ALTER TABLE "public"."agents" ADD CONSTRAINT "ck_agents_reasoning_level_enum" CHECK ("reasoning_level" IS NULL OR "reasoning_level" IN ('', 'low', 'medium', 'max'));
ALTER TABLE "public"."agents" ADD COLUMN "can_use_workers" boolean NOT NULL DEFAULT false;
ALTER TABLE "public"."agents" ADD COLUMN "worker_permissions" text NOT NULL DEFAULT '';
ALTER TABLE "public"."agents" ADD COLUMN "worker_allowed_mc_ps" text NOT NULL DEFAULT '';
ALTER TABLE "public"."agents" ADD CONSTRAINT "fk_agents_provider" FOREIGN KEY ("provider_id") REFERENCES "public"."llm_providers" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
ALTER TABLE "public"."agents" ADD CONSTRAINT "fk_agents_model_group" FOREIGN KEY ("model_group_id") REFERENCES "public"."model_groups" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- create "agent_mcp_servers" table
CREATE TABLE "public"."agent_mcp_servers" (
  "agent_id" integer NOT NULL,
  "mcp_server_id" integer NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  PRIMARY KEY ("agent_id", "mcp_server_id")
);
-- create "agent_mcp_accounts" table
CREATE TABLE "public"."agent_mcp_accounts" (
  "agent_id" integer NOT NULL,
  "mcp_account_id" integer NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  PRIMARY KEY ("agent_id", "mcp_account_id")
);
-- create "agent_mcp_tool_filters" table
CREATE TABLE "public"."agent_mcp_tool_filters" (
  "agent_id" integer NOT NULL,
  "mcp_server_id" integer NOT NULL,
  "tool_name" text NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  PRIMARY KEY ("agent_id", "mcp_server_id", "tool_name")
);
