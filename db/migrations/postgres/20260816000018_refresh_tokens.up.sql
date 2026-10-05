
-- create "refresh_tokens" table
CREATE TABLE "public"."refresh_tokens" (
  "id" serial NOT NULL,
  "family_id" text NOT NULL,
  "token_hash" text NOT NULL,
  "user_id" integer NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "absolute_expires_at" timestamptz NOT NULL,
  "used_at" timestamptz NULL,
  "revoked_at" timestamptz NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_refresh_tokens_user" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_refresh_tokens_family_id" to table: "refresh_tokens"
CREATE INDEX "idx_refresh_tokens_family_id" ON "public"."refresh_tokens" ("family_id");
-- create index "idx_refresh_tokens_token_hash" to table: "refresh_tokens"
CREATE UNIQUE INDEX "idx_refresh_tokens_token_hash" ON "public"."refresh_tokens" ("token_hash");
-- create index "idx_refresh_tokens_user_id" to table: "refresh_tokens"
CREATE INDEX "idx_refresh_tokens_user_id" ON "public"."refresh_tokens" ("user_id");
