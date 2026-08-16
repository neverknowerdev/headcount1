
-- create "git_hub_connections" table
CREATE TABLE "public"."git_hub_connections" (
  "id" serial NOT NULL,
  "installation_id" bigint NULL,
  "mcp_account_id" integer NULL,
  "user_id" integer NULL,
  "account_login" text NULL,
  "connected_at" timestamptz NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_git_hub_connections_installation_id" to table: "git_hub_connections"
CREATE INDEX "idx_git_hub_connections_installation_id" ON "public"."git_hub_connections" ("installation_id");
-- create index "idx_git_hub_connections_mcp_account_id" to table: "git_hub_connections"
CREATE INDEX "idx_git_hub_connections_mcp_account_id" ON "public"."git_hub_connections" ("mcp_account_id");
-- create index "idx_git_hub_connections_user_id" to table: "git_hub_connections"
CREATE INDEX "idx_git_hub_connections_user_id" ON "public"."git_hub_connections" ("user_id");
