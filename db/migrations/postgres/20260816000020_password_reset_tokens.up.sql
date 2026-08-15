
-- create "password_reset_tokens" table
CREATE TABLE "public"."password_reset_tokens" (
  "id" serial NOT NULL,
  "token_hash" text NOT NULL,
  "user_id" integer NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "used_at" timestamptz NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_password_reset_tokens_user" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_password_reset_tokens_token_hash" to table: "password_reset_tokens"
CREATE UNIQUE INDEX "idx_password_reset_tokens_token_hash" ON "public"."password_reset_tokens" ("token_hash");
-- create index "idx_password_reset_tokens_user_id" to table: "password_reset_tokens"
CREATE INDEX "idx_password_reset_tokens_user_id" ON "public"."password_reset_tokens" ("user_id");
