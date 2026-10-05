
-- create "agent_mcp_servers" table
CREATE TABLE "public"."agent_mcp_servers" (
  "agent_id" integer NOT NULL,
  "mcp_server_id" integer NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  PRIMARY KEY ("agent_id", "mcp_server_id")
);
