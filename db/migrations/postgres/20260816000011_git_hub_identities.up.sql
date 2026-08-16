
-- create "git_hub_identities" table
CREATE TABLE "public"."git_hub_identities" (
  "id" serial NOT NULL,
  "mcp_account_id" integer NOT NULL,
  "mcp_server_id" integer NOT NULL,
  "user_id" integer NOT NULL,
  "git_hub_user_id" bigint NOT NULL,
  "git_hub_login" text NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_git_hub_identities_mcp_account_id" to table: "git_hub_identities"
CREATE UNIQUE INDEX "idx_git_hub_identities_mcp_account_id" ON "public"."git_hub_identities" ("mcp_account_id");
-- create index "idx_github_identity" to table: "git_hub_identities"
CREATE UNIQUE INDEX "idx_github_identity" ON "public"."git_hub_identities" ("mcp_server_id", "user_id", "git_hub_user_id");
