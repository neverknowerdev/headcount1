
-- create "default_model_settings" table
CREATE TABLE "public"."default_model_settings" (
  "id" serial NOT NULL,
  "purpose" text NOT NULL,
  "user_id" integer NULL,
  "provider_id" integer NULL,
  "model" text NULL,
  "model_group_id" integer NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_default_model_settings_model_group" FOREIGN KEY ("model_group_id") REFERENCES "public"."model_groups" ("id") ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT "fk_default_model_settings_provider" FOREIGN KEY ("provider_id") REFERENCES "public"."llm_providers" ("id") ON UPDATE NO ACTION ON DELETE SET NULL
);
-- create index "idx_dms_user_purpose" to table: "default_model_settings"
CREATE UNIQUE INDEX "idx_dms_user_purpose" ON "public"."default_model_settings" ("purpose", "user_id");
