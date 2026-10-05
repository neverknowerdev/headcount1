
-- create "mcp_accounts" table
CREATE TABLE "public"."mcp_accounts" (
  "id" serial NOT NULL,
  "mcp_server_id" integer NOT NULL,
  "name" text NOT NULL,
  "auth_token" text NULL,
  "user_id" integer NULL,
  "last_error" text NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_mcp_servers_accounts" FOREIGN KEY ("mcp_server_id") REFERENCES "public"."mcp_servers" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- create index "idx_mcp_accounts_mcp_server_id" to table: "mcp_accounts"
CREATE INDEX "idx_mcp_accounts_mcp_server_id" ON "public"."mcp_accounts" ("mcp_server_id");
-- create index "idx_mcp_accounts_user_id" to table: "mcp_accounts"
CREATE INDEX "idx_mcp_accounts_user_id" ON "public"."mcp_accounts" ("user_id");
