
-- create "web_authn_sessions" table
CREATE TABLE "public"."web_authn_sessions" (
  "id" serial NOT NULL,
  "user_id" integer NULL,
  "purpose" text NOT NULL,
  "data" text NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_web_authn_sessions_expires_at" to table: "web_authn_sessions"
CREATE INDEX "idx_web_authn_sessions_expires_at" ON "public"."web_authn_sessions" ("expires_at");
-- create index "idx_web_authn_sessions_user_id" to table: "web_authn_sessions"
CREATE INDEX "idx_web_authn_sessions_user_id" ON "public"."web_authn_sessions" ("user_id");
