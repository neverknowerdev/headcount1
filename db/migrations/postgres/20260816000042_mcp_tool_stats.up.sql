
-- create "mcp_tool_stats" table
CREATE TABLE "public"."mcp_tool_stats" (
  "id" serial NOT NULL,
  "mcp_server_id" integer NOT NULL,
  "tool_name" text NOT NULL,
  "call_count" bigint NOT NULL DEFAULT 0,
  PRIMARY KEY ("id")
);
-- create index "idx_mcp_tool_stat" to table: "mcp_tool_stats"
CREATE UNIQUE INDEX "idx_mcp_tool_stat" ON "public"."mcp_tool_stats" ("mcp_server_id", "tool_name");
