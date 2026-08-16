
-- create "agent_mcp_tool_filters" table
CREATE TABLE "public"."agent_mcp_tool_filters" (
  "agent_id" integer NOT NULL,
  "mcp_server_id" integer NOT NULL,
  "tool_name" text NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  PRIMARY KEY ("agent_id", "mcp_server_id", "tool_name")
);
