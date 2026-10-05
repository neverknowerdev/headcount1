
-- create "user_git_credentials" table
CREATE TABLE "public"."user_git_credentials" (
  "id" serial NOT NULL,
  "user_id" integer NOT NULL,
  "ssh_private_key" text NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_user_git_credentials_user" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_user_git_credentials_user_id" to table: "user_git_credentials"
CREATE UNIQUE INDEX "idx_user_git_credentials_user_id" ON "public"."user_git_credentials" ("user_id");
