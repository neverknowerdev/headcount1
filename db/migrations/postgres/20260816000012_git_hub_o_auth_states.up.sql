
-- create "git_hub_o_auth_states" table
CREATE TABLE "public"."git_hub_o_auth_states" (
  "id" text NOT NULL,
  "redirect_url" text NULL,
  "mcp_server_id" integer NULL DEFAULT 0,
  "user_id" integer NULL DEFAULT 0,
  "mcp_account_id" integer NULL DEFAULT 0,
  "return_path" text NULL,
  "expires_at" timestamptz NULL,
  "used_at" timestamptz NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
