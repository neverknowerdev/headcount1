
-- create "users" table
CREATE TABLE "public"."users" (
  "id" serial NOT NULL,
  "email" text NOT NULL,
  "is_admin" boolean NOT NULL DEFAULT false,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "reenroll_token_hash" text NULL,
  "reenroll_expires_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_users_email" to table: "users"
CREATE UNIQUE INDEX "idx_users_email" ON "public"."users" ("email");
