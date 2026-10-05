CREATE UNIQUE INDEX IF NOT EXISTS "idx_github_connection_account_installation"
ON "public"."git_hub_connections" ("mcp_account_id", "installation_id");
