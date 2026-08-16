
-- create "model_group_members" table
CREATE TABLE "public"."model_group_members" (
  "id" serial NOT NULL,
  "group_id" integer NOT NULL,
  "provider_id" integer NOT NULL,
  "model" text NULL,
  "all_models" boolean NOT NULL DEFAULT false,
  "is_free" boolean NOT NULL DEFAULT false,
  "priority" bigint NOT NULL DEFAULT 0,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_model_group_members_provider" FOREIGN KEY ("provider_id") REFERENCES "public"."llm_providers" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_model_groups_members" FOREIGN KEY ("group_id") REFERENCES "public"."model_groups" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- create index "idx_model_group_members_group_id" to table: "model_group_members"
CREATE INDEX "idx_model_group_members_group_id" ON "public"."model_group_members" ("group_id");
