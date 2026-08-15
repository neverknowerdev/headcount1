
-- create "agent_mcp_accounts" table
CREATE TABLE "public"."agent_mcp_accounts" (
  "agent_id" integer NOT NULL,
  "mcp_account_id" integer NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  PRIMARY KEY ("agent_id", "mcp_account_id")
);
