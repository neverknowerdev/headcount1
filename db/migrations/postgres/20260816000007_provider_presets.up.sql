
-- create "provider_presets" table
CREATE TABLE "public"."provider_presets" (
  "id" serial NOT NULL,
  "key" text NOT NULL,
  "name" text NOT NULL,
  "base_url" text NOT NULL,
  "provider_type" text NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_provider_presets_key" to table: "provider_presets"
CREATE UNIQUE INDEX "idx_provider_presets_key" ON "public"."provider_presets" ("key");
