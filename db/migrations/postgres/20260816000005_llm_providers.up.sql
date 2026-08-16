
-- create "llm_providers" table
CREATE TABLE "public"."llm_providers" (
  "id" serial NOT NULL,
  "name" text NOT NULL,
  "base_url" text NOT NULL,
  "api_key" text NOT NULL,
  "user_id" integer NULL,
  "provider_type" text NULL,
  "default_model" text NULL,
  "supported_models" text NULL,
  "builtin" boolean NOT NULL DEFAULT false,
  "enabled" boolean NOT NULL DEFAULT true,
  "preset_key" text NULL DEFAULT '',
  "provider_name" text NULL DEFAULT '',
  "slug" text NULL DEFAULT '',
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_llm_providers_slug" to table: "llm_providers"
CREATE INDEX "idx_llm_providers_slug" ON "public"."llm_providers" ("slug");
-- create index "idx_llm_providers_user_id" to table: "llm_providers"
CREATE INDEX "idx_llm_providers_user_id" ON "public"."llm_providers" ("user_id");
