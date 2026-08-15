
-- create "mcp_servers" table
CREATE TABLE "public"."mcp_servers" (
  "id" serial NOT NULL,
  "name" text NOT NULL,
  "owner_user_id" integer NULL,
  "display_name" text NULL,
  "description" text NULL,
  "transport" text NOT NULL,
  "command" text NULL,
  "args" text NULL,
  "url" text NULL,
  "headers" text NULL,
  "auth_type" text NULL,
  "auth_env_var" text NULL,
  "tools_cache" text NULL,
  "last_error" text NULL,
  "init_status" text NULL DEFAULT '',
  "deps" text NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "builtin" boolean NOT NULL DEFAULT false,
  "work_dir" text NULL,
  "project_id" integer NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_mcp_servers_project" FOREIGN KEY ("project_id") REFERENCES "public"."projects" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_mcp_servers_name" to table: "mcp_servers"
CREATE UNIQUE INDEX "idx_mcp_servers_name" ON "public"."mcp_servers" ("name");
-- create index "idx_mcp_servers_owner_user_id" to table: "mcp_servers"
CREATE INDEX "idx_mcp_servers_owner_user_id" ON "public"."mcp_servers" ("owner_user_id");
-- create index "idx_mcp_servers_project_id" to table: "mcp_servers"
CREATE INDEX "idx_mcp_servers_project_id" ON "public"."mcp_servers" ("project_id");
