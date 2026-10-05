
-- create "sessions" table
CREATE TABLE "public"."sessions" (
  "id" serial NOT NULL,
  "token_hash" text NOT NULL,
  "user_id" integer NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "absolute_expires_at" timestamptz NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_sessions_user" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_sessions_token_hash" to table: "sessions"
CREATE UNIQUE INDEX "idx_sessions_token_hash" ON "public"."sessions" ("token_hash");
-- create index "idx_sessions_user_id" to table: "sessions"
CREATE INDEX "idx_sessions_user_id" ON "public"."sessions" ("user_id");
