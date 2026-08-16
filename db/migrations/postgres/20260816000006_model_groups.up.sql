
-- create "model_groups" table
CREATE TABLE "public"."model_groups" (
  "id" serial NOT NULL,
  "name" text NOT NULL,
  "slug" text NOT NULL,
  "user_id" integer NULL,
  "description" text NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_model_groups_slug" to table: "model_groups"
CREATE UNIQUE INDEX "idx_model_groups_slug" ON "public"."model_groups" ("slug");
-- create index "idx_model_groups_user_id" to table: "model_groups"
CREATE INDEX "idx_model_groups_user_id" ON "public"."model_groups" ("user_id");
